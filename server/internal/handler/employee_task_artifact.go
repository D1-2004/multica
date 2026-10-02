package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const employeeArtifactScheme = "employee-artifact-secretbox-v1"

var errEmployeeArtifactInvalid = errors.New("invalid employee artifact")
var errEmployeeArtifactBusy = errors.New("employee artifact upload is in progress")

type employeeArtifactBinding struct {
	WorkspaceID  string `json:"workspace_id"`
	AgentID      string `json:"agent_id"`
	TenantOrgID  string `json:"tenant_org_id"`
	SceneID      string `json:"scene_id"`
	TaskID       string `json:"task_id"`
	RunID        string `json:"run_id"`
	QueueTaskID  string `json:"queue_task_id"`
	GoalRevision int64  `json:"goal_revision"`
}
type employeeArtifact struct {
	ID                                    string
	Binding                               employeeArtifactBinding
	Filename, ContentType, SHA256         string
	SizeBytes, StoredSizeBytes            int64
	StorageKey, StorageURL, Scheme, State string
	LeaseToken                            pgtype.UUID
	LeaseExpiresAt                        pgtype.Timestamptz
	AttemptCount, TombstonePass           int
	CreatedAt                             time.Time
	ReadyAt                               pgtype.Timestamptz
}

const employeeArtifactColumns = `attachment_id::text,workspace_id::text,agent_id::text,tenant_org_id,scene_id::text,task_id::text,run_id::text,queue_task_id::text,goal_revision,filename,content_type,sha256,size_bytes,storage_key,storage_url,stored_size_bytes,encryption_scheme,state,lease_token,lease_expires_at,attempt_count,tombstone_pass,created_at,ready_at`

func scanEmployeeArtifact(row pgx.Row) (employeeArtifact, error) {
	var a employeeArtifact
	err := row.Scan(&a.ID, &a.Binding.WorkspaceID, &a.Binding.AgentID, &a.Binding.TenantOrgID, &a.Binding.SceneID, &a.Binding.TaskID, &a.Binding.RunID, &a.Binding.QueueTaskID, &a.Binding.GoalRevision, &a.Filename, &a.ContentType, &a.SHA256, &a.SizeBytes, &a.StorageKey, &a.StorageURL, &a.StoredSizeBytes, &a.Scheme, &a.State, &a.LeaseToken, &a.LeaseExpiresAt, &a.AttemptCount, &a.TombstonePass, &a.CreatedAt, &a.ReadyAt)
	return a, err
}

// EmployeeTaskArtifactRef is returned only after both object upload and the
// attachment/ready transaction have committed. URLs always require Direct ACL.
type EmployeeTaskArtifactRef struct {
	AttachmentResponse
	ArtifactRef string `json:"artifact_ref"`
	TaskID      string `json:"employee_task_id"`
	RunID       string `json:"run_id"`
	QueueTaskID string `json:"task_id"`
}

func (h *Handler) employeeArtifactRef(a employeeArtifact) EmployeeTaskArtifactRef {
	link := attachmentDownloadPath(a.ID)
	if base := strings.TrimRight(h.currentConfig().PublicURL, "/"); base != "" {
		link = base + link
	}
	return EmployeeTaskArtifactRef{AttachmentResponse: AttachmentResponse{ID: a.ID, WorkspaceID: a.Binding.WorkspaceID, UploaderType: "agent", UploaderID: a.Binding.AgentID, Filename: a.Filename, URL: link, DownloadURL: link, MarkdownURL: link, ContentType: a.ContentType, SizeBytes: a.SizeBytes, SHA256: a.SHA256, CreatedAt: a.CreatedAt.Format(time.RFC3339Nano)}, ArtifactRef: "attachment:" + a.ID, TaskID: a.Binding.TaskID, RunID: a.Binding.RunID, QueueTaskID: a.Binding.QueueTaskID}
}
func (h *Handler) loadEmployeeArtifactBinding(ctx context.Context, q db.DBTX, queueID string, upload bool) (employeeArtifactBinding, error) {
	var b employeeArtifactBinding
	var queueState, runState, taskState, activeRunID string
	if upload {
		// Match completion's workspace -> queue lock order; a terminal transition
		// cannot commit between checking the run and publishing its ready artifact.
		var id pgtype.UUID
		if err := q.QueryRow(ctx, `SELECT id FROM agent_task_queue WHERE id=$1::uuid FOR SHARE`, queueID).Scan(&id); err != nil {
			return b, err
		}
	}
	err := q.QueryRow(ctx, `SELECT t.workspace_id::text,t.agent_id::text,t.tenant_org_id,t.scene_id::text,t.id::text,r.id::text,q.id::text,r.goal_revision,q.status,r.state,t.state,COALESCE(t.active_run_id::text,'')
 FROM agent_task_queue q JOIN employee_task_run r ON r.queue_task_id=q.id
 JOIN employee_task t ON t.id=r.task_id AND t.workspace_id=r.workspace_id AND t.agent_id=r.agent_id AND t.tenant_org_id=r.tenant_org_id
 WHERE q.id=$1::uuid AND q.context->>'type'='employee_direct' AND q.context->>'workspace_id'=t.workspace_id::text AND q.context->>'employee_task_id'=t.id::text
 AND q.agent_id=t.agent_id AND t.owner_loop='employee' AND t.dispatch_mode='direct' AND t.scope_kind='scene'
 AND q.issue_id IS NULL AND q.chat_session_id IS NULL AND q.autopilot_run_id IS NULL`, queueID).Scan(&b.WorkspaceID, &b.AgentID, &b.TenantOrgID, &b.SceneID, &b.TaskID, &b.RunID, &b.QueueTaskID, &b.GoalRevision, &queueState, &runState, &taskState, &activeRunID)
	if err != nil {
		return b, err
	}
	if upload && (queueState != "running" || runState != "running" || taskState != "running" || activeRunID != b.RunID) {
		return b, errEmployeeArtifactInvalid
	}
	owner := scene.Owner{WorkspaceID: parseUUID(b.WorkspaceID), AgentID: parseUUID(b.AgentID)}
	directory, err := scene.Get(ctx, db.New(q), owner, parseUUID(b.SceneID))
	if err != nil {
		return b, err
	}
	currentOrg, err := agentTenantOrg(ctx, db.New(q), owner, b.TenantOrgID)
	if err != nil {
		return b, err
	}
	if err := scene.CheckTenant(directory, currentOrg); err != nil {
		return b, err
	}
	return b, nil
}
func (h *Handler) employeeArtifactForAttachment(ctx context.Context, att db.Attachment) (employeeArtifact, bool, error) {
	a, err := scanEmployeeArtifact(h.DB.QueryRow(ctx, `SELECT `+employeeArtifactColumns+` FROM employee_task_artifact WHERE attachment_id=$1`, att.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		if att.TaskID.Valid {
			task, e := h.Queries.GetAgentTask(ctx, att.TaskID)
			if e != nil {
				// Existing Issue/Chat files survive queue cleanup. A missing Direct
				// binding is fail-closed only for unbound task-scoped attachments.
				if errors.Is(e, pgx.ErrNoRows) && (att.IssueID.Valid || att.CommentID.Valid || att.ChatSessionID.Valid || att.ChatMessageID.Valid) {
					return a, false, nil
				}
				return a, true, e
			}
			if service.IsEmployeeDirectTask(task) {
				return a, true, errEmployeeArtifactInvalid
			}
		}
		return a, false, nil
	}
	if err != nil {
		return a, true, err
	}
	if a.State != "ready" || a.Binding.WorkspaceID != uuidToString(att.WorkspaceID) || a.Binding.QueueTaskID != uuidToString(att.TaskID) || a.Filename != att.Filename || a.SHA256 != att.Sha256 || a.SizeBytes != att.SizeBytes || att.IssueID.Valid || att.CommentID.Valid || att.ChatSessionID.Valid || att.ChatMessageID.Valid {
		return a, true, errEmployeeArtifactInvalid
	}
	binding, err := h.loadEmployeeArtifactBinding(ctx, h.DB, a.Binding.QueueTaskID, false)
	if err != nil || binding != a.Binding {
		return a, true, errEmployeeArtifactInvalid
	}
	return a, true, nil
}
func (h *Handler) authorizeEmployeeArtifact(r *http.Request, att db.Attachment) (employeeArtifact, bool, error) {
	a, direct, err := h.employeeArtifactForAttachment(r.Context(), att)
	if !direct || err != nil {
		return a, direct, err
	}
	task, err := h.Queries.GetAgentTask(r.Context(), att.TaskID)
	if err != nil || !h.canReadDaemonEmployeeTask(r, task) {
		return a, true, service.ErrDirectTaskAccessDenied
	}
	return a, true, nil
}
func (h *Handler) checkEmployeeArtifactAccess(w http.ResponseWriter, r *http.Request, att db.Attachment) bool {
	_, _, err := h.authorizeEmployeeArtifact(r, att)
	if err == nil {
		return true
	}
	writeError(w, http.StatusNotFound, "attachment not found")
	return false
}

type employeeArtifactEnvelope struct {
	Scheme  string                  `json:"scheme"`
	ID      string                  `json:"id"`
	Binding employeeArtifactBinding `json:"binding"`
	SHA256  string                  `json:"sha256"`
	Data    []byte                  `json:"data"`
}

func (h *Handler) sealEmployeeArtifact(a employeeArtifact, data []byte) ([]byte, error) {
	box := h.contextCredentialBox()
	if box == nil {
		return nil, errors.New("artifact encryption unavailable")
	}
	raw, err := json.Marshal(employeeArtifactEnvelope{employeeArtifactScheme, a.ID, a.Binding, a.SHA256, data})
	if err != nil {
		return nil, err
	}
	return box.Seal(raw)
}
func (h *Handler) openEmployeeArtifact(a employeeArtifact, sealed []byte) ([]byte, error) {
	box := h.contextCredentialBox()
	if box == nil || a.Scheme != employeeArtifactScheme {
		return nil, errEmployeeArtifactInvalid
	}
	raw, err := box.Open(sealed)
	if err != nil {
		return nil, errEmployeeArtifactInvalid
	}
	var envelope employeeArtifactEnvelope
	if json.Unmarshal(raw, &envelope) != nil || envelope.Scheme != a.Scheme || envelope.ID != a.ID || envelope.Binding != a.Binding || envelope.SHA256 != a.SHA256 || int64(len(envelope.Data)) != a.SizeBytes || sha256Hex(envelope.Data) != a.SHA256 {
		return nil, errEmployeeArtifactInvalid
	}
	return envelope.Data, nil
}
func employeeArtifactFilename(filename string) (string, error) {
	name := path.Base(filename)
	if name == "." || name == "/" || name == "" || len(name) > 512 || !utf8.ValidString(name) || strings.ContainsAny(name, "\r\n\x00") {
		return "", fmt.Errorf("%w: filename", errEmployeeArtifactInvalid)
	}
	return name, nil
}

// ListEmployeeTaskArtifacts is the Host notice query. scope and run are taken
// from a verified completion job; model paths and URLs cannot create references.
func (h *Handler) ListEmployeeTaskArtifacts(ctx context.Context, scope employeetask.Scope, taskID, runID string) ([]EmployeeTaskArtifactRef, error) {
	rows, err := h.DB.Query(ctx, `SELECT queue_task_id::text FROM employee_task_run WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND id=$5::uuid`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, taskID, runID)
	if err != nil {
		return nil, err
	}
	queueID := ""
	for rows.Next() {
		if err = rows.Scan(&queueID); err != nil {
			rows.Close()
			return nil, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if queueID == "" {
		return nil, pgx.ErrNoRows
	}
	binding, err := h.loadEmployeeArtifactBinding(ctx, h.DB, queueID, false)
	if err != nil || binding.SceneID != scope.Scene.SceneID || scope.Kind != employeetask.ScopeScene {
		return nil, errEmployeeArtifactInvalid
	}
	artifacts, err := h.DB.Query(ctx, `SELECT `+employeeArtifactColumns+` FROM employee_task_artifact WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND run_id=$5::uuid AND state='ready'
 AND EXISTS(SELECT 1 FROM attachment a WHERE a.id=employee_task_artifact.attachment_id AND a.workspace_id=employee_task_artifact.workspace_id AND a.task_id=employee_task_artifact.queue_task_id
  AND a.sha256=employee_task_artifact.sha256 AND a.filename=employee_task_artifact.filename AND a.size_bytes=employee_task_artifact.size_bytes
  AND a.issue_id IS NULL AND a.comment_id IS NULL AND a.chat_session_id IS NULL AND a.chat_message_id IS NULL)
 ORDER BY created_at,attachment_id`, scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, taskID, runID)
	if err != nil {
		return nil, err
	}
	defer artifacts.Close()
	out := []EmployeeTaskArtifactRef{}
	for artifacts.Next() {
		a, err := scanEmployeeArtifact(artifacts)
		if err != nil {
			return nil, err
		}
		if a.Binding != binding {
			return nil, errEmployeeArtifactInvalid
		}
		out = append(out, h.employeeArtifactRef(a))
	}
	return out, artifacts.Err()
}
