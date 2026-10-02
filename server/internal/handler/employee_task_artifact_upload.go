package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) uploadEmployeeTaskArtifact(w http.ResponseWriter, r *http.Request, task db.AgentTaskQueue, filename, contentType string, data []byte) {
	if h.contextCredentialBox() == nil {
		writeError(w, http.StatusServiceUnavailable, "artifact encryption is unavailable")
		return
	}
	if r.Header.Get("X-Actor-Source") != "task_token" || !h.canExecuteEmployeeDirectTask(r, task) {
		writeError(w, http.StatusForbidden, "task token does not match artifact source")
		return
	}
	if r.FormValue("issue_id") != "" || r.FormValue("comment_id") != "" || r.FormValue("chat_session_id") != "" {
		writeError(w, http.StatusBadRequest, "Direct artifacts cannot bind to an Issue or Chat")
		return
	}
	name, err := employeeArtifactFilename(filename)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid artifact filename")
		return
	}
	a, err := h.reserveEmployeeArtifact(r.Context(), task, name, contentType, data)
	if err != nil {
		status := http.StatusConflict
		if !errors.Is(err, errEmployeeArtifactBusy) && !errors.Is(err, errEmployeeArtifactInvalid) {
			status = http.StatusInternalServerError
		}
		writeError(w, status, "artifact upload could not be reserved")
		return
	}
	if a.State == "ready" {
		writeJSON(w, http.StatusOK, h.employeeArtifactRef(a))
		return
	}
	sealed, err := h.sealEmployeeArtifact(a, data)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "artifact encryption is unavailable")
		return
	}
	uploadCtx, cancelUpload := context.WithTimeout(r.Context(), time.Minute)
	_, err = h.Storage.Upload(uploadCtx, a.StorageKey, sealed, "application/octet-stream", "artifact.enc")
	cancelUpload()
	if err != nil {
		// Ambiguous PUTs stay in the durable ledger for retry/reconciliation.
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		_, _ = h.DB.Exec(releaseCtx, `UPDATE employee_task_artifact SET lease_token=NULL,lease_expires_at=NULL,last_error='object upload failed',updated_at=now() WHERE attachment_id=$1::uuid AND state='pending' AND lease_token=$2`, a.ID, a.LeaseToken)
		writeError(w, http.StatusBadGateway, "artifact upload failed")
		return
	}
	a.StoredSizeBytes = int64(len(sealed))
	if err = h.finishEmployeeArtifact(r.Context(), a); err != nil {
		writeError(w, http.StatusConflict, "artifact upload was not committed")
		return
	}
	writeJSON(w, http.StatusOK, h.employeeArtifactRef(a))
}
func (h *Handler) reserveEmployeeArtifact(ctx context.Context, task db.AgentTaskQueue, name, contentType string, data []byte) (employeeArtifact, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return employeeArtifact{}, err
	}
	defer tx.Rollback(ctx)
	workspaceID := h.TaskService.ResolveTaskWorkspaceID(ctx, task)
	var workspace pgtype.UUID
	if err = tx.QueryRow(ctx, `SELECT id FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, workspaceID).Scan(&workspace); err != nil {
		return employeeArtifact{}, err
	}
	binding, err := h.loadEmployeeArtifactBinding(ctx, tx, uuidToString(task.ID), true)
	if err != nil {
		return employeeArtifact{}, err
	}
	id := uuid.NewString()
	key := "workspaces/" + binding.WorkspaceID + "/employee-artifacts/" + id + ".enc"
	digest := sha256Hex(data)
	_, err = tx.Exec(ctx, `INSERT INTO employee_task_artifact(attachment_id,workspace_id,agent_id,tenant_org_id,scene_id,task_id,run_id,queue_task_id,goal_revision,filename,content_type,sha256,size_bytes,storage_key,storage_url,next_attempt_at)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6::uuid,$7::uuid,$8::uuid,$9,$10,$11,$12,$13,$14,$15,now()+interval '10 minutes') ON CONFLICT(workspace_id,queue_task_id,filename,sha256) DO NOTHING`, id, binding.WorkspaceID, binding.AgentID, binding.TenantOrgID, binding.SceneID, binding.TaskID, binding.RunID, binding.QueueTaskID, binding.GoalRevision, name, contentType, digest, len(data), key, h.Storage.ObjectURL(key))
	if err != nil {
		return employeeArtifact{}, err
	}
	a, err := scanEmployeeArtifact(tx.QueryRow(ctx, `SELECT `+employeeArtifactColumns+` FROM employee_task_artifact WHERE workspace_id=$1::uuid AND queue_task_id=$2::uuid AND filename=$3 AND sha256=$4 FOR UPDATE`, workspaceID, binding.QueueTaskID, name, digest))
	if err != nil {
		return a, err
	}
	if a.Binding != binding || a.State == "deleting" || a.State == "tombstoned" {
		return a, errEmployeeArtifactInvalid
	}
	if a.State == "ready" {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM attachment WHERE id=$1::uuid AND workspace_id=$2::uuid AND task_id=$3::uuid)`, a.ID, binding.WorkspaceID, binding.QueueTaskID).Scan(&exists); err != nil || !exists {
			return a, errEmployeeArtifactInvalid
		}
		return a, tx.Commit(ctx)
	}
	if a.LeaseToken.Valid && a.LeaseExpiresAt.Valid && a.LeaseExpiresAt.Time.After(time.Now()) {
		return a, errEmployeeArtifactBusy
	}
	a.LeaseToken = pgtype.UUID{Bytes: uuid.New(), Valid: true}
	if _, err = tx.Exec(ctx, `UPDATE employee_task_artifact SET lease_token=$2,lease_expires_at=now()+interval '2 minutes',next_attempt_at=now()+interval '10 minutes',attempt_count=attempt_count+1,last_error='',updated_at=now() WHERE attachment_id=$1::uuid`, a.ID, a.LeaseToken); err != nil {
		return a, err
	}
	return a, tx.Commit(ctx)
}
func (h *Handler) finishEmployeeArtifact(ctx context.Context, a employeeArtifact) error {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var workspace pgtype.UUID
	if err = tx.QueryRow(ctx, `SELECT id FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, a.Binding.WorkspaceID).Scan(&workspace); err != nil {
		return err
	}
	binding, err := h.loadEmployeeArtifactBinding(ctx, tx, a.Binding.QueueTaskID, true)
	if err != nil || binding != a.Binding {
		return errEmployeeArtifactInvalid
	}
	current, err := scanEmployeeArtifact(tx.QueryRow(ctx, `SELECT `+employeeArtifactColumns+` FROM employee_task_artifact WHERE attachment_id=$1::uuid FOR UPDATE`, a.ID))
	if err != nil {
		return err
	}
	if current.State != "pending" || current.LeaseToken != a.LeaseToken || !current.LeaseExpiresAt.Valid || !current.LeaseExpiresAt.Time.After(time.Now()) {
		return errEmployeeArtifactInvalid
	}
	_, err = db.New(tx).CreateAttachment(ctx, db.CreateAttachmentParams{ID: parseUUID(a.ID), WorkspaceID: parseUUID(a.Binding.WorkspaceID), TaskID: parseUUID(a.Binding.QueueTaskID), UploaderType: "agent", UploaderID: parseUUID(a.Binding.AgentID), Filename: a.Filename, Url: a.StorageURL, ContentType: a.ContentType, SizeBytes: a.SizeBytes, Sha256: a.SHA256})
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE employee_task_artifact SET state='ready',stored_size_bytes=$2,ready_at=now(),lease_token=NULL,lease_expires_at=NULL,last_error='',updated_at=now() WHERE attachment_id=$1::uuid`, a.ID, a.StoredSizeBytes); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
