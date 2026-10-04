package handler

import (
	"bytes"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"io"
	"net/http"
	"time"

	"github.com/multica-ai/multica/server/internal/storage"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) serveEmployeeArtifact(w http.ResponseWriter, r *http.Request, att db.Attachment, preview bool) bool {
	a, direct, err := h.authorizeEmployeeArtifact(r, att)
	if !direct {
		return false
	}
	if err != nil {
		writeError(w, http.StatusNotFound, "attachment not found")
		return true
	}
	if h.Storage == nil || h.contextCredentialBox() == nil {
		writeError(w, http.StatusServiceUnavailable, "artifact storage unavailable")
		return true
	}
	if preview && (!isTextPreviewable(a.ContentType, a.Filename) || a.SizeBytes > maxPreviewTextSize) {
		writeError(w, http.StatusUnsupportedMediaType, "artifact preview unavailable")
		return true
	}
	reader, err := h.Storage.GetReader(r.Context(), a.StorageKey)
	if err != nil {
		writeError(w, http.StatusNotFound, "artifact object not found")
		return true
	}
	defer reader.Close()
	if a.StoredSizeBytes < 28 || a.StoredSizeBytes > maxUploadSize*2 {
		writeError(w, http.StatusInternalServerError, "artifact object is invalid")
		return true
	}
	sealed, err := io.ReadAll(io.LimitReader(reader, a.StoredSizeBytes+1))
	if err != nil || int64(len(sealed)) != a.StoredSizeBytes {
		writeError(w, http.StatusInternalServerError, "artifact object is incomplete")
		return true
	}
	plain, err := h.openEmployeeArtifact(a, sealed)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "artifact object is invalid")
		return true
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	h.setAttachmentPreviewSecurityHeaders(w)
	if preview {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Original-Content-Type", a.ContentType)
	} else {
		w.Header().Set("Content-Type", a.ContentType)
		w.Header().Set("Content-Disposition", storage.AttachmentContentDisposition(a.Filename))
	}
	http.ServeContent(w, r, a.Filename, time.Time{}, bytes.NewReader(plain))
	return true
}

// ListEmployeeTaskArtifactsByUser applies the existing Direct task read policy
// before listing only ready, immutable run artifacts.
func (h *Handler) ListEmployeeTaskArtifactsByUser(w http.ResponseWriter, r *http.Request) {
	task, ok := h.requireUserTaskViewAccess(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	if !service.IsEmployeeDirectTask(task) {
		writeError(w, http.StatusNotFound, "Direct task not found")
		return
	}
	binding, err := h.loadEmployeeArtifactBinding(r.Context(), h.DB, uuidToString(task.ID), false)
	if err != nil {
		writeError(w, http.StatusNotFound, "artifact source not found")
		return
	}
	scope := employeetask.Scope{WorkspaceID: binding.WorkspaceID, AgentID: binding.AgentID, TenantOrgID: binding.TenantOrgID, Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: binding.SceneID}}
	refs, err := h.ListEmployeeTaskArtifacts(r.Context(), scope, binding.TaskID, binding.RunID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list task artifacts")
		return
	}
	writeJSON(w, http.StatusOK, refs)
}

func (h *Handler) deleteEmployeeTaskArtifact(w http.ResponseWriter, r *http.Request, att db.Attachment) bool {
	a, direct, err := h.authorizeEmployeeArtifact(r, att)
	if !direct {
		return false
	}
	if err != nil {
		writeError(w, http.StatusNotFound, "attachment not found")
		return true
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete artifact")
		return true
	}
	defer tx.Rollback(r.Context())
	var workspace pgtype.UUID
	if err = tx.QueryRow(r.Context(), `SELECT id FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, a.Binding.WorkspaceID).Scan(&workspace); err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE employee_task_artifact SET state='deleting',lease_token=NULL,lease_expires_at=NULL,next_attempt_at=now(),updated_at=now() WHERE attachment_id=$1::uuid AND workspace_id=$2::uuid AND state='ready'`, a.ID, a.Binding.WorkspaceID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM attachment WHERE id=$1::uuid AND workspace_id=$2::uuid`, a.ID, a.Binding.WorkspaceID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete artifact")
		return true
	}
	w.WriteHeader(http.StatusNoContent)
	return true
}
