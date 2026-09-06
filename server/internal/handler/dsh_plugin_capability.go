package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/util"
)

// Serving a stored plugin package to a sandbox.
//
// The runtime adapter fetches a plugin with a plain GET and no Authorization
// header, so the credential has to live in the URL. That is the same problem
// attachment downloads already solved, and this mirrors their capability
// signature rather than inventing a second scheme.
//
// A presigned object-store URL was the obvious alternative and does not work
// here: the deployment's object endpoint is VPC-internal, so a URL signed
// against it is unreachable from the sandbox, and the public endpoint rejects
// the same signature. Serving through the API keeps the object read on the
// server, where the internal endpoint is exactly right.

const (
	dshPluginCapabilityDomain  = "dsh-plugin-artifact-v1"
	dshPluginCapabilityVersion = "v1"
	// dshPluginCapabilityTTL bounds a link. A sandbox fetches within seconds of
	// the task claim; this leaves room for a slow cold start without leaving a
	// usable link sitting in an environment variable for long.
	dshPluginCapabilityTTL = 30 * time.Minute
	// maxDshArtifactServeBytes bounds what this route will stream.
	maxDshArtifactServeBytes = 64 << 20
)

var (
	dshPluginCapabilityKeyOnce sync.Once
	dshPluginCapabilityKey     []byte
)

// dshPluginCapabilitySigningKey derives a key from the deployment's JWT secret,
// exactly as attachment capabilities do. Deriving rather than reusing means a
// plugin signature can never collide with a JWT or an attachment signature, and
// rotating JWT_SECRET invalidates outstanding links.
func dshPluginCapabilitySigningKey() []byte {
	dshPluginCapabilityKeyOnce.Do(func() {
		sum := sha256.Sum256(append([]byte(dshPluginCapabilityDomain), auth.JWTSecret()...))
		dshPluginCapabilityKey = sum[:]
	})
	return dshPluginCapabilityKey
}

// signDshPluginCapability returns the hex HMAC over the capability's fields.
// The fields are joined with a separator that cannot occur inside a UUID or a
// decimal timestamp, so no other pair of values re-splits to the same message.
func signDshPluginCapability(pluginID string, exp int64) string {
	mac := hmac.New(sha256.New, dshPluginCapabilitySigningKey())
	mac.Write([]byte(dshPluginCapabilityVersion))
	mac.Write([]byte("|"))
	mac.Write([]byte(pluginID))
	mac.Write([]byte("|"))
	mac.Write([]byte(strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyDshPluginCapability fails closed on every path: a missing field, an
// unparseable or elapsed expiry, a malformed signature, and a signature minted
// for a different plugin all return false. The signature covers the claimed
// expiry, so extending it invalidates the link rather than the reverse.
func verifyDshPluginCapability(pluginID, rawExp, rawSig string, now time.Time) bool {
	if pluginID == "" || rawExp == "" || rawSig == "" {
		return false
	}
	exp, err := strconv.ParseInt(rawExp, 10, 64)
	if err != nil || now.Unix() > exp {
		return false
	}
	got, err := hex.DecodeString(rawSig)
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(signDshPluginCapability(pluginID, exp))
	if err != nil {
		return false
	}
	return hmac.Equal(got, want)
}

// dshPluginArtifactURL is the absolute link handed to a sandbox.
//
// Absolute, not site-relative: the consumer is a process in a sandbox with no
// notion of an API base, unlike a browser resolving `download_url`.
func (h *Handler) dshPluginArtifactURL(pluginID string, now time.Time) string {
	base := strings.TrimRight(strings.TrimSpace(h.currentConfig().PublicURL), "/")
	if base == "" {
		return ""
	}
	exp := now.Add(dshPluginCapabilityTTL).Unix()
	return base + "/api/dsh-plugins/" + pluginID + "/artifact" +
		"?exp=" + strconv.FormatInt(exp, 10) +
		"&sig=" + signDshPluginCapability(pluginID, exp)
}

// DownloadDshPluginArtifact streams a stored plugin package.
//
// Public by necessity and by design: the caller is a sandbox process that sends
// no Authorization header. The short-lived, single-plugin signature in the
// query is the credential, and it is only ever minted while composing a task
// for an agent that is already bound to that plugin.
func (h *Handler) DownloadDshPluginArtifact(w http.ResponseWriter, r *http.Request) {
	pluginID := chi.URLParam(r, "id")
	query := r.URL.Query()
	if !verifyDshPluginCapability(pluginID, query.Get("exp"), query.Get("sig"), time.Now()) {
		writeError(w, http.StatusForbidden, "invalid or expired plugin link")
		return
	}
	pluginUUID, err := util.ParseUUID(pluginID)
	if err != nil {
		writeError(w, http.StatusNotFound, "plugin not found")
		return
	}
	// The signature already established which plugin this is, so the row is
	// looked up by id alone — there is no authenticated workspace to scope by,
	// and scoping by one supplied in the query would be no check at all.
	row, err := h.Queries.GetDshPluginByID(r.Context(), pluginUUID)
	if err != nil || row.ArtifactKey == "" {
		writeError(w, http.StatusNotFound, "plugin package not found")
		return
	}
	if h.Storage == nil {
		writeError(w, http.StatusServiceUnavailable, "plugin storage is unavailable")
		return
	}
	reader, err := h.Storage.GetReader(r.Context(), row.ArtifactKey)
	if err != nil {
		slog.Error("failed to read a DSH plugin artifact",
			"plugin_id", pluginID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to read the plugin package")
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Cache-Control", "private, no-store")
	if row.ArtifactSize > 0 && row.ArtifactSize <= maxDshArtifactServeBytes {
		w.Header().Set("Content-Length", strconv.FormatInt(row.ArtifactSize, 10))
	}
	if _, err := io.Copy(w, io.LimitReader(reader, maxDshArtifactServeBytes)); err != nil {
		slog.Warn("DSH plugin artifact stream ended early",
			"plugin_id", pluginID, "error", err)
	}
}
