package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

var (
	errTaskEventInvalid  = errors.New("invalid task event")
	errTaskEventConflict = errors.New("task event sequence conflict")
)

// persistTaskMessageBatch serializes reporters on the existing queue row. The
// non-unique historical transcript index can be used without deleting old rows
// to install a unique index. Identity is frozen from host data, not the request.
func (h *Handler) persistTaskMessageBatch(ctx context.Context, task db.AgentTaskQueue, workspaceID string, messages []TaskMessageRequest) ([]db.TaskMessage, error) {
	messages = append([]TaskMessageRequest(nil), messages...)
	if len(messages) > 2000 {
		return nil, errTaskEventInvalid
	}
	previous := 0
	for i := range messages {
		m := &messages[i]
		if m.Seq <= previous || m.Seq > math.MaxInt32 || len(m.Type) > 64 || m.Type == "" {
			return nil, errTaskEventInvalid
		}
		if (m.Type == "text" || m.Type == "thinking") && m.Content == "" {
			return nil, errTaskEventInvalid
		}
		previous = m.Seq
		if m.Event != nil {
			if len(m.Event.SessionID) > 1024 {
				return nil, errTaskEventInvalid
			}
			for _, s := range []string{m.Event.TurnID, m.Event.MessageID, m.Event.CallID, m.Event.Phase, m.Event.Status, m.Event.Level} {
				if len(s) > 256 {
					return nil, errTaskEventInvalid
				}
			}
			source := *m.Event
			source.SessionID, source.TurnID, source.MessageID, source.CallID = redact.Text(source.SessionID), redact.Text(source.TurnID), redact.Text(source.MessageID), redact.Text(source.CallID)
			source.Phase, source.Status, source.Level = redact.Text(source.Phase), redact.Text(source.Status), redact.Text(source.Level)
			m.Event = &source
		}
		m.Content, m.Output, m.Tool = redact.Text(m.Content), redact.Text(m.Output), redact.Text(m.Tool)
		m.Input = redact.InputMap(m.Input)
	}
	if h.TxStarter == nil {
		return nil, errors.New("task event transaction unavailable")
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	// Completion uses workspace -> queue locks. Preserve that order.
	var locked pgtype.UUID
	if err = tx.QueryRow(ctx, `SELECT id FROM workspace WHERE id=$1 FOR KEY SHARE`, parseUUID(workspaceID)).Scan(&locked); err != nil {
		return nil, err
	}
	if err = tx.QueryRow(ctx, `SELECT id FROM agent_task_queue WHERE id=$1 FOR UPDATE`, task.ID).Scan(&locked); err != nil {
		return nil, err
	}
	q := db.New(tx)
	current, err := q.GetAgentTask(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	var host protocol.TaskEventContext
	if h.TaskRunEventsEnabled {
		host, err = h.taskEventContext(ctx, tx, current, workspaceID)
		if err != nil {
			return nil, err
		}
	}
	created := make([]db.TaskMessage, 0, len(messages))
	for _, m := range messages {
		existing, e := q.GetTaskMessageBySeq(ctx, db.GetTaskMessageBySeqParams{TaskID: task.ID, Seq: int32(m.Seq)})
		if e == nil {
			if !sameTaskMessage(existing, m) {
				return nil, fmt.Errorf("%w: seq %d", errTaskEventConflict, m.Seq)
			}
			continue
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return nil, e
		}
		var input, event []byte
		if m.Input != nil {
			input, err = json.Marshal(m.Input)
			if err != nil {
				return nil, err
			}
		}
		if h.TaskRunEventsEnabled {
			meta := host
			if m.Event != nil {
				meta.Source = *m.Event
			}
			if meta.Source.SessionID == "" && current.SessionID.Valid {
				meta.Source.SessionID = current.SessionID.String
			}
			event, err = json.Marshal(meta)
			if err != nil {
				return nil, err
			}
		}
		row, e := q.CreateTaskMessageEvent(ctx, db.CreateTaskMessageEventParams{TaskID: task.ID, Seq: int32(m.Seq), Type: m.Type, Tool: pgtype.Text{String: m.Tool, Valid: m.Tool != ""}, Content: pgtype.Text{String: m.Content, Valid: m.Content != ""}, Input: input, Output: pgtype.Text{String: m.Output, Valid: m.Output != ""}, Event: event})
		if e != nil {
			return nil, e
		}
		created = append(created, row)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return created, nil
}

func sameTaskMessage(old db.TaskMessage, m TaskMessageRequest) bool {
	var input map[string]any
	if len(old.Input) > 0 && json.Unmarshal(old.Input, &input) != nil {
		return false
	}
	var incoming map[string]any
	if m.Input != nil {
		raw, err := json.Marshal(m.Input)
		if err != nil || json.Unmarshal(raw, &incoming) != nil {
			return false
		}
	}
	if old.Type != m.Type || old.Tool.String != m.Tool || old.Content.String != m.Content || old.Output.String != m.Output || !reflect.DeepEqual(input, incoming) {
		return false
	}
	// A legacy reporter has no metadata. Server-enriched session identities may
	// change after pinning; compare only observations that the reporter supplied.
	if m.Event != nil && len(old.Event) > 0 {
		var host protocol.TaskEventContext
		if json.Unmarshal(old.Event, &host) != nil {
			return false
		}
		prior := host.Source
		if m.Event.SessionID == "" {
			prior.SessionID = ""
		}
		return reflect.DeepEqual(prior, *m.Event)
	}
	return true
}

func (h *Handler) taskEventContext(ctx context.Context, tx pgx.Tx, task db.AgentTaskQueue, ws string) (protocol.TaskEventContext, error) {
	m := protocol.TaskEventContext{Version: 1, WorkspaceID: ws, AgentID: uuidToString(task.AgentID), RuntimeID: uuidToString(task.RuntimeID)}
	q := db.New(tx)
	if task.RuntimeID.Valid {
		runtime, err := q.GetAgentRuntime(ctx, task.RuntimeID)
		if err != nil {
			return m, err
		}
		if uuidToString(runtime.WorkspaceID) != ws {
			return m, errTaskEventInvalid
		}
		m.Provider = runtime.Provider
	}
	var accepted struct {
		Scene scene.Ref `json:"agent_scene"`
	}
	if len(task.Context) > 0 && string(task.Context) != "null" && json.Unmarshal(task.Context, &accepted) != nil {
		return m, errTaskEventInvalid
	}
	if accepted.Scene.SceneID != "" {
		id, err := scene.ParseID(accepted.Scene.SceneID)
		if err != nil {
			return m, errTaskEventInvalid
		}
		directory, err := scene.Get(ctx, q, scene.Owner{WorkspaceID: parseUUID(ws), AgentID: task.AgentID}, id)
		if err != nil {
			return m, err
		}
		m.SceneID = scene.RefOf(directory).SceneID
	}
	// A queue context cannot manufacture an Employee run association.
	var employeeSceneID string
	err := tx.QueryRow(ctx, `SELECT t.id::text,r.id::text,r.goal_revision,COALESCE(t.scene_id::text,'') FROM employee_task_run r JOIN employee_task t ON t.id=r.task_id AND t.workspace_id=r.workspace_id AND t.agent_id=r.agent_id AND t.tenant_org_id=r.tenant_org_id WHERE r.queue_task_id=$1 AND t.workspace_id=$2 AND t.agent_id=$3`, task.ID, parseUUID(ws), task.AgentID).Scan(&m.TaskID, &m.RunID, &m.GoalRevision, &employeeSceneID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return m, err
	}
	if err == nil && employeeSceneID != m.SceneID {
		return m, errTaskEventInvalid
	}
	return m, nil
}

// taskRunEvent exposes facts usable for Tone, never diagnostic bodies or raw
// tool output. A provider final marker is not a platform completion fact.
func taskRunEvent(row db.TaskMessage) protocol.TaskRunEvent {
	e := protocol.TaskRunEvent{EventID: fmt.Sprintf("%s:%d", uuidToString(row.TaskID), row.Seq), QueueTaskID: uuidToString(row.TaskID), Seq: int(row.Seq), Kind: "diagnostic", Role: "runtime", Reportability: "none", CreatedAt: row.CreatedAt.Time.UTC().Format(time.RFC3339Nano)}
	_ = json.Unmarshal(row.Event, &e.TaskEventContext)
	switch row.Type {
	case "text":
		e.Kind, e.Role, e.Reportability, e.Content = "assistant_text", "assistant", "progress", row.Content.String
	case "tool_use":
		e.Kind, e.Role, e.Reportability, e.Tool = "tool_started", "tool", "progress", row.Tool.String
	case "tool_result":
		e.Kind, e.Role, e.Reportability, e.Tool = "tool_finished", "tool", "progress", row.Tool.String
	case "error":
		e.Kind, e.Reportability, e.Content = "error", "attention", row.Content.String
	case "status":
		e.Kind = "status"
		if e.Source.Status == "running" || e.Source.Status == "turn_complete" || e.Source.Status == "step_complete" {
			e.Reportability = "progress"
		}
	}
	return e
}

func (h *Handler) ListDaemonTaskRunEvents(w http.ResponseWriter, r *http.Request) {
	if !h.TaskRunEventsEnabled {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	task, ws, ok := h.requireDaemonTaskAccessWithWorkspace(w, r, chi.URLParam(r, "taskId"))
	if !ok || !h.requireDaemonEmployeeTaskRead(w, r, task) {
		return
	}
	h.listTaskRunEvents(w, r, task, ws)
}

func (h *Handler) ListTaskRunEventsByUser(w http.ResponseWriter, r *http.Request) {
	if !h.TaskRunEventsEnabled {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	task, ok := h.requireUserTaskMessageRead(w, r)
	if !ok {
		return
	}
	h.listTaskRunEvents(w, r, task, h.TaskService.ResolveTaskWorkspaceID(r.Context(), task))
}

func (h *Handler) listTaskRunEvents(w http.ResponseWriter, r *http.Request, task db.AgentTaskQueue, ws string) {
	since, limit := 0, 500
	for name, target := range map[string]*int{"since": &since, "limit": &limit} {
		if raw := r.URL.Query().Get(name); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 || n > math.MaxInt32 || (name == "limit" && (n < 1 || n > 1000)) {
				writeError(w, http.StatusBadRequest, "invalid event cursor or limit")
				return
			}
			*target = n
		}
	}
	if h.TxStarter == nil {
		writeError(w, http.StatusInternalServerError, "event read transaction unavailable")
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "event read transaction unavailable")
		return
	}
	defer tx.Rollback(context.WithoutCancel(r.Context()))
	q := db.New(tx)
	// Task authorization does not permit a stale scene binding to cross tenants.
	var accepted struct {
		Scene scene.Ref `json:"agent_scene"`
	}
	if len(task.Context) > 0 && string(task.Context) != "null" && json.Unmarshal(task.Context, &accepted) != nil {
		writeError(w, http.StatusInternalServerError, "invalid task context")
		return
	}
	if accepted.Scene.SceneID != "" {
		id, err := scene.ParseID(accepted.Scene.SceneID)
		if err != nil {
			writeError(w, http.StatusNotFound, "scene not found")
			return
		}
		owner := scene.Owner{WorkspaceID: parseUUID(ws), AgentID: task.AgentID}
		var locked pgtype.UUID
		if err = tx.QueryRow(r.Context(), `SELECT id FROM agent_scene WHERE workspace_id=$1 AND agent_id=$2 AND id=$3 FOR SHARE`, owner.WorkspaceID, owner.AgentID, id).Scan(&locked); err != nil {
			writeTaskEventSceneError(w, err)
			return
		}
		directory, err := scene.Get(r.Context(), q, owner, id)
		if err != nil {
			writeTaskEventSceneError(w, err)
			return
		}
		var identityOrg string
		err = tx.QueryRow(r.Context(), `SELECT org_id FROM agent_dingtalk_identity WHERE workspace_id=$1 AND agent_id=$2 FOR SHARE`, owner.WorkspaceID, owner.AgentID).Scan(&identityOrg)
		if err != nil && (!errors.Is(err, pgx.ErrNoRows) || service.IsEmployeeDirectTask(task)) {
			writeTaskEventSceneError(w, err)
			return
		}
		// Keep secondary-tenant removal from racing the use-time fence.
		if identityOrg != "" && identityOrg != directory.TenantOrgID {
			var org string
			if e := tx.QueryRow(r.Context(), `SELECT org_id FROM agent_tenant WHERE workspace_id=$1 AND agent_id=$2 AND org_id=$3 FOR SHARE`, owner.WorkspaceID, owner.AgentID, directory.TenantOrgID).Scan(&org); e != nil {
				writeTaskEventSceneError(w, e)
				return
			}
		}
		org, err := agentTenantOrg(r.Context(), q, owner, directory.TenantOrgID)
		if err == nil {
			err = scene.CheckTenant(directory, org)
		}
		if err != nil {
			writeTaskEventSceneError(w, err)
			return
		}
	}
	wantedScene, sessionID := strings.TrimSpace(r.URL.Query().Get("scene_id")), r.URL.Query().Get("session_id")
	if len(sessionID) > 1024 {
		writeError(w, http.StatusBadRequest, "invalid session_id")
		return
	}
	if wantedScene != "" && wantedScene != accepted.Scene.SceneID {
		writeError(w, http.StatusNotFound, "scene not found")
		return
	}
	rows, err := q.ListTaskRunEvents(r.Context(), db.ListTaskRunEventsParams{TaskID: task.ID, Seq: int32(since), WorkspaceID: ws, SceneID: wantedScene, SessionID: sessionID, PageLimit: int32(limit + 1)})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read task events")
		return
	}
	p := protocol.TaskRunEventPage{Events: make([]protocol.TaskRunEvent, 0, limit), NextSeq: since, HasMore: len(rows) > limit}
	if p.HasMore {
		rows = rows[:limit]
	}
	for _, row := range rows {
		p.Events = append(p.Events, taskRunEvent(row))
		p.NextSeq = int(row.Seq)
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read task events")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func writeTaskEventSceneError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, scene.ErrNotFound) || errors.Is(err, scene.ErrStaleTenant) || errors.Is(err, scene.ErrUnresolved) {
		writeError(w, http.StatusForbidden, "task scene is no longer active")
		return
	}
	writeError(w, http.StatusInternalServerError, "failed to read task scene")
}
