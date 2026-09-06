package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/dshplugin"
	"github.com/multica-ai/multica/server/internal/storage"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Multica keeps the exact bytes it validated, and hands the sandbox a
// short-lived link to them.
//
// Fetching from npm at every task start makes plugin loading only as reliable
// as a registry, adds a round trip to the internet on the critical path, and
// means the sandbox can end up running bytes nobody validated if the registry
// serves something new under the same spec. Storing the artifact removes all
// three, and it is the only way an uploaded package — which has no upstream URL
// at all — can be delivered.
//
// The link is a presigned object-store GET, so the runtime adapter needs no
// change: it already accepts an https tarball and verifies the recorded digest.

const (
	// dshArtifactLinkTTL bounds a presigned link. A sandbox fetches within
	// seconds of the task claim; this leaves room for a slow start without
	// leaving a usable link lying around in an environment variable.
	dshArtifactLinkTTL = 30 * time.Minute
	// maxDshPluginUploadBytes bounds the multipart body.
	maxDshPluginUploadBytes = dshplugin.MaxUploadBytes
)

// dshPluginArtifactKey is the object key for one plugin's stored package.
// Keyed by content digest so re-importing identical bytes reuses one object,
// and so a key never has to be reused for different content.
func dshPluginArtifactKey(workspaceID pgtype.UUID, packageName, digestHex string) string {
	safe := strings.ReplaceAll(packageName, "/", "__")
	return fmt.Sprintf("dsh-plugins/%s/%s/%s.tgz", uuidToString(workspaceID), safe, digestHex)
}

// storeDshPluginArtifact uploads the validated bytes and returns the key.
// An empty key with a nil error means storage is not configured, which is not
// fatal for a plugin that has an upstream source to fall back on.
func (h *Handler) storeDshPluginArtifact(
	ctx context.Context, workspaceID pgtype.UUID, packageName string, data []byte,
) (string, error) {
	if h.Storage == nil {
		return "", nil
	}
	sum := sha256.Sum256(data)
	key := dshPluginArtifactKey(workspaceID, packageName, hex.EncodeToString(sum[:]))
	if _, err := h.Storage.Upload(ctx, key, data, "application/gzip", packageName+".tgz"); err != nil {
		return "", err
	}
	return key, nil
}

// dshPluginDeliverySource is the source the sandbox should fetch.
//
// A presigned link to the stored artifact when one is available, otherwise the
// recorded upstream spec. An uploaded package has no upstream, so if the link
// cannot be produced it is better to say so than to emit a spec that the
// adapter will reject with a confusing error.
func (h *Handler) dshPluginDeliverySource(ctx context.Context, row db.DshPlugin) (string, error) {
	if row.ArtifactKey != "" {
		if presigner, ok := h.Storage.(storage.Presigner); ok && presigner != nil {
			url, err := presigner.PresignGet(ctx, row.ArtifactKey, dshArtifactLinkTTL)
			if err == nil && url != "" {
				return url, nil
			}
			if err != nil {
				slog.Warn("failed to presign a DSH plugin artifact",
					"package", row.PackageName, "error", err)
			}
		}
	}
	if row.SourceKind == string(dshplugin.SourceUpload) {
		return "", fmt.Errorf("plugin %s was uploaded and its stored package is unavailable", row.PackageName)
	}
	return row.SourceSpec, nil
}

// UploadDshPlugin imports a plugin from an uploaded .zip or .tgz.
//
// Whatever the file's layout — npm's `package/` wrapper, a GitHub "Download
// ZIP" nested under `<repo>-<ref>/`, or a plain folder — it is normalised to
// one shape first and then held to exactly the same gates as a package fetched
// from a registry. There is deliberately only one validation path.
func (h *Handler) UploadDshPlugin(w http.ResponseWriter, r *http.Request) {
	workspaceUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	creatorID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	if h.Storage == nil {
		writeError(w, http.StatusServiceUnavailable,
			"uploading a plugin needs object storage, which is not configured here")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxDshPluginUploadBytes+(1<<20))
	if err := r.ParseMultipartForm(maxDshPluginUploadBytes); err != nil {
		writeError(w, http.StatusBadRequest, "invalid upload, or the file exceeds the size limit")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, `a plugin archive is required (form field "file")`)
		return
	}
	defer file.Close()

	raw, err := io.ReadAll(io.LimitReader(file, maxDshPluginUploadBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read the uploaded file")
		return
	}
	if int64(len(raw)) > maxDshPluginUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("the upload is larger than the %d byte limit", int64(maxDshPluginUploadBytes)))
		return
	}

	filename := ""
	if header != nil {
		filename = header.Filename
	}
	normalized, err := dshplugin.NormalizeUpload(raw, filename)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	resolved, err := dshplugin.InspectUploaded(normalized)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	sum := sha256.Sum256(normalized)
	resolved.Integrity = "sha256-" + hex.EncodeToString(sum[:])

	existing, err := h.Queries.ListDshPluginsByWorkspace(r.Context(), workspaceUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read the workspace plugin set")
		return
	}
	if len(existing) >= maxDshPluginsPerWorkspace {
		writeError(w, http.StatusUnprocessableEntity, "this workspace already has the maximum number of DSH plugins")
		return
	}

	artifactKey, err := h.storeDshPluginArtifact(r.Context(), workspaceUUID, resolved.PackageName, normalized)
	if err != nil || artifactKey == "" {
		slog.Error("failed to store an uploaded DSH plugin",
			"package", resolved.PackageName, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to store the uploaded package")
		return
	}

	configRow, err := chooseConfigRow("", nil, resolved)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.persistDshPlugin(w, r, dshPluginWrite{
		WorkspaceID: workspaceUUID,
		CreatorID:   parseUUID(creatorID),
		Source: dshplugin.Source{
			Kind: dshplugin.SourceUpload,
			// The spec records provenance for a human; it is never fetched,
			// because an uploaded package is delivered from storage.
			Spec: "upload:" + strings.TrimSpace(filename),
		},
		Resolved:     resolved,
		ConfigRow:    configRow,
		OnConflict:   strings.TrimSpace(r.FormValue("on_conflict")),
		DisplayName:  strings.TrimSpace(r.FormValue("display_name")),
		ArtifactKey:  artifactKey,
		ArtifactSize: int64(len(normalized)),
	})
}

// DshPluginUpdateResponse reports whether a newer version is published.
type DshPluginUpdateResponse struct {
	PackageName     string `json:"package_name"`
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	// Checkable is false for a source with no upstream to compare against.
	Checkable  bool   `json:"checkable"`
	Reason     string `json:"reason"`
	SourceSpec string `json:"source_spec"`
}

// CheckDshPluginUpdate reports the newest published version of a plugin.
//
// Only an npm source can be checked: a pinned GitHub ref and an uploaded file
// have no notion of "newer", and saying so is more useful than silently
// reporting no update.
func (h *Handler) CheckDshPluginUpdate(w http.ResponseWriter, r *http.Request) {
	row, ok := h.loadDshPlugin(w, r)
	if !ok {
		return
	}
	resp := DshPluginUpdateResponse{
		PackageName:    row.PackageName,
		CurrentVersion: row.ResolvedVersion,
		SourceSpec:     row.SourceSpec,
	}
	if row.SourceKind != string(dshplugin.SourceNPM) {
		resp.Reason = "only a plugin installed from npm can be checked for updates"
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Checkable = true

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	latest, err := dshPluginResolver().LatestVersion(ctx, row.PackageName)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"code":  "upstream_unavailable",
			"error": err.Error(),
		})
		return
	}
	resp.LatestVersion = latest
	resp.UpdateAvailable = latest != "" && latest != row.ResolvedVersion
	writeJSON(w, http.StatusOK, resp)
}
