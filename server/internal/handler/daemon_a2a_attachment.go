package handler

import (
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/storage"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DownloadDaemonA2AAttachment streams one input object to the daemon that owns
// the local turn. It is separate from the member-facing file API: an inbound
// A2A child has no Multica task token and cannot call this endpoint.
func (h *Handler) DownloadDaemonA2AAttachment(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	task, workspaceID, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, taskID)
	if !ok {
		return
	}
	if !service.IsA2ATaskOrigin(task.Context) || h.Storage == nil {
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}
	attachmentID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "attachmentId"), "attachment_id")
	if !ok {
		return
	}
	attachment, err := h.Queries.GetAttachment(r.Context(), db.GetAttachmentParams{
		ID: attachmentID, WorkspaceID: parseUUID(workspaceID),
	})
	if err != nil || !attachment.TaskID.Valid || attachment.TaskID.Bytes != task.ID.Bytes {
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}
	reader, err := h.Storage.GetReader(r.Context(), h.Storage.KeyFromURL(attachment.Url))
	if err != nil {
		writeError(w, http.StatusNotFound, "attachment not found")
		return
	}
	defer reader.Close()
	contentType := attachment.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", storage.ContentDisposition(contentType, attachment.Filename))
	w.Header().Set("Content-Length", strconv.FormatInt(attachment.SizeBytes, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, reader)
}
