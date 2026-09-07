package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/util"
)

// Serving a stored plugin package to a sandbox.
//
// The sandbox has no route to Multica's public origin. The only Multica it can
// reach is the loopback egress relay the daemon hands it as MULTICA_SERVER_URL,
// so the server cannot name a host at all — it sends a path, and the adapter
// joins it to the origin it was actually given.
//
// The relay forwards an /api/ request only when it carries a task-scoped mat_
// bearer, so the adapter attaches that too. The signature below is the second
// of the two checks and the only one bound to a specific plugin: the bearer
// says "a running task", the signature says "this package".
//
// A presigned object-store URL was tried first and is the wrong shape here. The
// deployment's object endpoint is VPC-internal, so a URL signed against it is
// unreachable from the sandbox; the public endpoint is reachable but returned
// 403, which probing showed to be bucket policy or credential expiry rather
// than anything about the signature format. Serving through the API sidesteps
// all of it and keeps the object read on the server, where the internal
// endpoint is exactly right.

const (
	dshPluginCapabilityDomain  = "dsh-plugin-artifact-v1"
	dshPluginCapabilityVersion = "v1"
	// dshPluginCapabilityTTL bounds a link. A sandbox fetches within seconds of
	// the task claim, and this leaves room for a slow cold start; anything
	// longer just widens the window in which a leaked environment variable is
	// still worth something.
	dshPluginCapabilityTTL = 5 * time.Minute
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

// dshPluginArtifactSource is the plugin source handed to a sandbox.
//
// Deliberately origin-less. The server's own public URL is not reachable from a
// sandbox, and the address that is — a per-task loopback relay — is not
// something the server knows. So this names only the path, under a scheme the
// adapter recognises as "resolve against the server URL you were given".
func (h *Handler) dshPluginArtifactSource(pluginID string, now time.Time) string {
	exp := now.Add(dshPluginCapabilityTTL).Unix()
	return "multica:/api/dsh-plugins/" + pluginID + "/artifact" +
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
	// Same hardening as the attachment capability route this mirrors: never let
	// a stored package be sniffed into something a browser would execute, and
	// never leak the signed URL onward through a referrer.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Disposition", "attachment")
	if row.ArtifactSize > 0 && row.ArtifactSize <= maxDshArtifactServeBytes {
		w.Header().Set("Content-Length", strconv.FormatInt(row.ArtifactSize, 10))
	}
	if _, err := io.Copy(w, io.LimitReader(reader, maxDshArtifactServeBytes)); err != nil {
		slog.Warn("DSH plugin artifact stream ended early",
			"plugin_id", pluginID, "error", err)
	}
}
