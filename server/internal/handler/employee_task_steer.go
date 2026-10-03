package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type EmployeeTaskSteerResponse struct {
	TaskID                   string                           `json:"task_id"`
	Outcome                  string                           `json:"outcome"`
	RunID                    string                           `json:"run_id,omitempty"`
	QueueTaskID              string                           `json:"queue_task_id,omitempty"`
	QueueStatus              string                           `json:"queue_status,omitempty"`
	InterruptedQueueTaskID   string                           `json:"interrupted_queue_task_id,omitempty"`
	EntrySeq                 int64                            `json:"entry_seq,omitempty"`
	CommentID                string                           `json:"comment_id,omitempty"`
	Replayed                 bool                             `json:"replayed"`
	Capabilities             employeetask.ControlCapabilities `json:"capabilities"`
	SuccessorAwaitsExitProof bool                             `json:"successor_awaits_exit_proof"`
}

// SteerEmployeeTask is the human entry to the Task Service steer capability.
// {id} is an EmployeeTask ID or the queue task ID of one of its Runs. The
// correction interrupts the current Run and resumes the task with it.
func (h *Handler) SteerEmployeeTask(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" || h.TxStarter == nil {
		writeError(w, http.StatusBadRequest, "workspace is required")
		return
	}
	if _, ok := h.workspaceMember(w, r, workspaceID); !ok {
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid correction")
		return
	}
	req.Content = strings.TrimSpace(sanitizeNullBytes(req.Content))
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if req.Content == "" || len(req.Content) > service.EmployeeTaskSteerMaxBytes || key == "" || len(key) > 200 {
		writeError(w, http.StatusBadRequest, "content (at most 16000 bytes) and Idempotency-Key (at most 200 bytes) are required")
		return
	}
	task, ok := h.loadEmployeeTaskForSteer(w, r, workspaceID, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: parseUUID(task.Scope.AgentID), WorkspaceID: parseUUID(workspaceID)})
	if err != nil || agent.ArchivedAt.Valid || !h.canInvokeAgent(r.Context(), agent, "member", userID, userID, workspaceID) {
		writeError(w, http.StatusForbidden, "cannot invoke this task's agent")
		return
	}
	sameRequester := false
	if task.DispatchMode == employeetask.DispatchDirect {
		latest, err := h.latestEmployeeTaskQueue(r, task)
		if err != nil {
			writeError(w, http.StatusConflict, "task has no execution to steer")
			return
		}
		// Direct output and inputs belong to the requester; only that requester
		// or a manager of the agent may redirect the execution.
		if !h.canReadEmployeeDirectTask(r, latest, agent) {
			writeError(w, http.StatusForbidden, "you cannot control this task")
			return
		}
		sameRequester = latest.OriginatorUserID.Valid && uuidToString(latest.OriginatorUserID) == userID
	}
	control := service.EmployeeTaskControl{Tasks: h.TaskService, Issues: service.NewEmployeeIssueBackend(h.IssueService, h.IssueCommentService)}
	result, err := control.Steer(r.Context(), service.EmployeeTaskSteerRequest{
		Task:          task,
		Source:        employeetask.Source{Namespace: "human_steer", Key: userID + ":" + key},
		ActorRef:      "member:" + userID,
		Content:       req.Content,
		AuthorID:      parseUUID(userID),
		SameRequester: sameRequester,
	})
	if err != nil {
		writeEmployeeTaskSteerError(w, err)
		return
	}
	if result.Interrupted != nil && result.Interrupted.IssueID.Valid {
		h.reconcileCommentsOnCompletion(r.Context(), result.Interrupted)
	}
	resp := EmployeeTaskSteerResponse{
		TaskID: task.ID, Outcome: string(result.Outcome), RunID: result.Run.ID, EntrySeq: result.Entry.Seq,
		Replayed: result.Replayed, Capabilities: employeetask.Capabilities(task.DispatchMode),
	}
	if result.Queue.ID.Valid {
		resp.QueueTaskID = uuidToString(result.Queue.ID)
		resp.QueueStatus = result.Queue.Status
	}
	if result.Interrupted != nil {
		resp.InterruptedQueueTaskID = uuidToString(result.Interrupted.ID)
		resp.SuccessorAwaitsExitProof = true
	}
	if result.CommentID.Valid {
		resp.CommentID = uuidToString(result.CommentID)
	}
	writeJSON(w, http.StatusAccepted, resp)
}

func writeEmployeeTaskSteerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, employeetask.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid correction")
	case errors.Is(err, employeetask.ErrNotFound):
		writeError(w, http.StatusNotFound, "employee task not found")
	case errors.Is(err, employeetask.ErrRunNotReady):
		writeError(w, http.StatusConflict, "the previous execution has not proven that it stopped")
	case errors.Is(err, employeetask.ErrConflict), errors.Is(err, employeetask.ErrActiveRun):
		writeError(w, http.StatusConflict, "task state changed or the request conflicts with an earlier one")
	case errors.Is(err, service.ErrEmployeeTaskSteerUnsupported):
		writeError(w, http.StatusConflict, "this task's backend does not support steer")
	case errors.Is(err, service.ErrDirectTaskAccessDenied):
		writeError(w, http.StatusForbidden, "you cannot control this task")
	case strings.HasPrefix(err.Error(), "direct task agent unavailable"), strings.HasPrefix(err.Error(), "direct task runtime lacks"):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "failed to steer task")
	}
}

// loadEmployeeTaskForSteer resolves an EmployeeTask in this workspace by its
// own ID or by the queue task ID of one of its Runs.
func (h *Handler) loadEmployeeTaskForSteer(w http.ResponseWriter, r *http.Request, workspaceID, ref string) (employeetask.Task, bool) {
	if _, err := util.ParseUUID(ref); err != nil {
		writeError(w, http.StatusBadRequest, "invalid task id")
		return employeetask.Task{}, false
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "employee task storage is unavailable")
		return employeetask.Task{}, false
	}
	defer tx.Rollback(r.Context())
	var scope employeetask.Scope
	var id string
	err = tx.QueryRow(r.Context(), `SELECT t.id::text,t.workspace_id::text,t.agent_id::text,t.tenant_org_id,t.scope_kind,COALESCE(t.scene_id::text,''),COALESCE(t.legacy_id::text,'')
 FROM employee_task t WHERE t.workspace_id=$1::uuid AND (t.id=$2::uuid OR EXISTS(SELECT 1 FROM employee_task_run r WHERE r.task_id=t.id AND r.workspace_id=t.workspace_id AND r.queue_task_id=$2::uuid)) LIMIT 1`, workspaceID, ref).Scan(&id, &scope.WorkspaceID, &scope.AgentID, &scope.TenantOrgID, &scope.Kind, &scope.Scene.SceneID, &scope.LegacyID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "employee task not found")
		return employeetask.Task{}, false
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "employee task lookup failed")
		return employeetask.Task{}, false
	}
	task, err := employeetask.NewStore(tx).Get(r.Context(), scope, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "employee task not found")
		return employeetask.Task{}, false
	}
	return task, true
}

func (h *Handler) latestEmployeeTaskQueue(r *http.Request, task employeetask.Task) (db.AgentTaskQueue, error) {
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	defer tx.Rollback(r.Context())
	run, err := employeetask.NewStore(tx).LatestRun(r.Context(), task.Scope, task.ID)
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	var queueID pgtype.UUID
	if queueID, err = util.ParseUUID(run.QueueTaskID); err != nil {
		return db.AgentTaskQueue{}, err
	}
	return h.Queries.GetAgentTask(r.Context(), queueID)
}
