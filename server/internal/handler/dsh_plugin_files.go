package handler

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/dshplugin"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Reading what is actually inside an imported plugin.
//
// A plugin is code that will run beside the agent's own tools, so "what did I
// just install" has to be answerable without leaving Multica — especially for
// an uploaded package, which exists nowhere else to go and look at.
//
// The listing and the file bodies are read out of the stored package on demand
// rather than copied into the database at import. The archive is bounded at
// import, reads are rare, and keeping one copy means the bytes shown here are
// by construction the bytes the sandbox runs.

const (
	// maxDshPluginFileBytes bounds a single file returned for viewing. Larger
	// files still appear in the listing, marked as not viewable.
	maxDshPluginFileBytes = 512 << 10
	// maxDshPluginListedFiles bounds the listing.
	maxDshPluginListedFiles = 2000
)

// DshPluginFileEntry is one file inside a plugin package.
type DshPluginFileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	// Viewable is false for a binary file or one past the size cap; the client
	// shows it in the tree but does not offer to open it.
	Viewable bool `json:"viewable"`
}

// DshPluginFilesResponse is the package's contents.
type DshPluginFilesResponse struct {
	PackageName string               `json:"package_name"`
	Version     string               `json:"resolved_version"`
	Files       []DshPluginFileEntry `json:"files"`
	// Truncated is true when the package holds more files than are listed.
	Truncated bool `json:"truncated"`
}

// loadDshPluginArchive fetches the stored package bytes for one plugin.
func (h *Handler) loadDshPluginArchive(ctx context.Context, row db.DshPlugin) ([]byte, error) {
	if h.Storage == nil {
		return nil, errNoStoredPackage
	}
	key := row.ArtifactKey
	if key == "" {
		var err error
		key, err = dshplugin.StoredArchiveKey(uuidToString(row.WorkspaceID), row.PackageName, row.Integrity)
		if err != nil {
			return nil, errNoStoredPackage
		}
	}
	reader, err := h.Storage.GetReader(ctx, key)
	if err != nil {
		if row.ArtifactKey == "" {
			return nil, errNoStoredPackage
		}
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(io.LimitReader(reader, dshplugin.MaxUploadBytes+1))
}

var errNoStoredPackage = &noStoredPackageError{}

type noStoredPackageError struct{}

func (e *noStoredPackageError) Error() string { return "no stored package" }

// ListDshPluginFiles returns every file in the plugin's package.
func (h *Handler) ListDshPluginFiles(w http.ResponseWriter, r *http.Request) {
	row, ok := h.loadDshPlugin(w, r)
	if !ok {
		return
	}
	data, err := h.loadDshPluginArchive(r.Context(), row)
	if err != nil {
		if err == errNoStoredPackage {
			writeError(w, http.StatusNotFound,
				"this plugin has no stored package, so its contents cannot be shown")
			return
		}
		slog.Error("failed to read a DSH plugin package for listing",
			"plugin", row.PackageName, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to read the plugin package")
		return
	}
	files, err := dshplugin.ArchiveContents(data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	truncated := false
	if len(paths) > maxDshPluginListedFiles {
		paths = paths[:maxDshPluginListedFiles]
		truncated = true
	}
	entries := make([]DshPluginFileEntry, 0, len(paths))
	for _, path := range paths {
		body := files[path]
		entries = append(entries, DshPluginFileEntry{
			Path:     path,
			Size:     int64(len(body)),
			Viewable: len(body) <= maxDshPluginFileBytes && isProbablyText(body),
		})
	}
	writeJSON(w, http.StatusOK, DshPluginFilesResponse{
		PackageName: row.PackageName,
		Version:     row.ResolvedVersion,
		Files:       entries,
		Truncated:   truncated,
	})
}

// GetDshPluginFile returns one file's text.
func (h *Handler) GetDshPluginFile(w http.ResponseWriter, r *http.Request) {
	row, ok := h.loadDshPlugin(w, r)
	if !ok {
		return
	}
	wanted := strings.TrimSpace(r.URL.Query().Get("path"))
	if wanted == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	data, err := h.loadDshPluginArchive(r.Context(), row)
	if err != nil {
		if err == errNoStoredPackage {
			writeError(w, http.StatusNotFound,
				"this plugin has no stored package, so its contents cannot be shown")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to read the plugin package")
		return
	}
	files, err := dshplugin.ArchiveContents(data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Exact match against the listing's own keys. No path joining happens here,
	// so there is nothing to traverse out of — the archive reader already
	// rejected unsafe paths when the package was imported.
	body, found := files[wanted]
	if !found {
		writeError(w, http.StatusNotFound, "that file is not in this package")
		return
	}
	if len(body) > maxDshPluginFileBytes {
		writeError(w, http.StatusUnprocessableEntity, "that file is too large to display")
		return
	}
	if !isProbablyText(body) {
		writeError(w, http.StatusUnprocessableEntity, "that file is not text")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":    wanted,
		"size":    len(body),
		"content": string(body),
	})
}

// isProbablyText reports whether a file can be shown as text. A NUL byte is
// the practical signal for binary, and invalid UTF-8 would not survive JSON
// encoding intact.
func isProbablyText(body []byte) bool {
	if len(body) == 0 {
		return true
	}
	probe := body
	if len(probe) > 8192 {
		probe = probe[:8192]
	}
	for _, b := range probe {
		if b == 0 {
			return false
		}
	}
	return utf8.Valid(probe)
}
