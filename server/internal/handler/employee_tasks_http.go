package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/employeeverification"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/taskinput"
	"github.com/multica-ai/multica/server/internal/util"
)

// The /api/employee-tasks read API projects an EmployeeTask, never a queue
// row or an Issue: task_id, run_id, queue_task_id and issue_id are distinct
// identities. Readers are the Task's originator, a manager of its agent, or
// the exact execution credential of one of its Runs. Collection answers,
// other participants' data, memory and credentials are never included; free
// text is redacted, and an invisible Task is indistinguishable from a missing
// one (404).

const (
	employeeTaskListDefault  = 20
	employeeTaskListMax      = 50
	employeeTaskEntryDefault = 50
	employeeTaskEntryMax     = 100
	employeeTaskRunDefault   = 20
	employeeTaskRunMax       = 50
)

// employeeTaskReader is the authenticated caller of the read API.
type employeeTaskReader struct {
	workspaceID string
	// Human member credential.
	userID    string
	manageAll bool
	// Server-stamped task token: the exact queue execution it may read.
	queueID string
	agentID string
}

// visible renders the reader's visibility predicate over employee_task t,
// numbering placeholders from next.
func (rd employeeTaskReader) visible(next int) (string, []any) {
	p := func(i int) string { return "$" + strconv.Itoa(next+i) }
	if rd.queueID != "" {
		return `(t.workspace_id=` + p(0) + `::uuid AND t.agent_id=` + p(1) + `::uuid AND EXISTS(SELECT 1 FROM employee_task_run vr WHERE vr.workspace_id=t.workspace_id AND vr.task_id=t.id AND vr.queue_task_id=` + p(2) + `::uuid))`,
			[]any{rd.workspaceID, rd.agentID, rd.queueID}
	}
	user := p(2)
	return `(t.workspace_id=` + p(0) + `::uuid AND (` + p(1) + `::bool
 OR EXISTS(SELECT 1 FROM agent va WHERE va.id=t.agent_id AND va.workspace_id=t.workspace_id AND va.owner_id=` + user + `::uuid)
 OR t.requester_ref IN ('member:'||` + user + `::text, 'issue_creator:member:'||` + user + `::text)
 OR EXISTS(SELECT 1 FROM employee_task_run vr JOIN agent_task_queue vq ON vq.id=vr.queue_task_id WHERE vr.workspace_id=t.workspace_id AND vr.task_id=t.id AND vq.originator_user_id=` + user + `::uuid)
 OR (t.source_namespace='employee_scene' AND EXISTS(SELECT 1 FROM employee_event_consumption vc WHERE vc.workspace_id=t.workspace_id AND vc.agent_id=t.agent_id AND vc.tenant_org_id=t.tenant_org_id AND vc.scene_id=t.scene_id AND vc.owner_loop='employee' AND vc.principal_id=` + user + `::uuid AND vc.receipt_id::text=split_part(t.source_key,'/',1)))))`,
		[]any{rd.workspaceID, rd.manageAll, rd.userID}
}

// employeeTaskReaderFor authenticates the caller. Machine credentials other
// than the exact task token never take the human path.
func (h *Handler) employeeTaskReaderFor(w http.ResponseWriter, r *http.Request) (employeeTaskReader, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" || h.TxStarter == nil {
		writeError(w, http.StatusBadRequest, "workspace is required")
		return employeeTaskReader{}, false
	}
	if _, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id"); !ok {
		return employeeTaskReader{}, false
	}
	if r.Header.Get("X-Actor-Source") == "task_token" {
		queueID, agentID := r.Header.Get("X-Task-ID"), r.Header.Get("X-Agent-ID")
		if r.Header.Get("X-Workspace-ID") != workspaceID || !validUUIDString(queueID) || !validUUIDString(agentID) {
			writeError(w, http.StatusForbidden, "this credential cannot read employee tasks")
			return employeeTaskReader{}, false
		}
		return employeeTaskReader{workspaceID: workspaceID, queueID: queueID, agentID: agentID}, true
	}
	if !isEmployeeHumanCredential(r) {
		writeError(w, http.StatusForbidden, "this credential cannot read employee tasks")
		return employeeTaskReader{}, false
	}
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return employeeTaskReader{}, false
	}
	return employeeTaskReader{workspaceID: workspaceID, userID: uuidToString(member.UserID), manageAll: roleAllowed(member.Role, "owner", "admin")}, true
}

// validUUIDString checks an untrusted UUID (headers, cursors, query values).
func validUUIDString(s string) bool {
	id, err := util.ParseUUID(s)
	return err == nil && id.Valid && uuidToString(id) == s
}

// readTx opens a repeatable-read transaction so one response is one snapshot.
func (h *Handler) employeeTaskReadTx(ctx context.Context) (pgx.Tx, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// loadReadableEmployeeTask resolves the path UUID to a Task the reader may see.
func loadReadableEmployeeTask(ctx context.Context, tx pgx.Tx, rd employeeTaskReader, id string) (employeetask.Task, error) {
	pred, args := rd.visible(2)
	var scope employeetask.Scope
	var taskID string
	err := tx.QueryRow(ctx, `SELECT t.id::text,t.workspace_id::text,t.agent_id::text,t.tenant_org_id,t.scope_kind,COALESCE(t.scene_id::text,''),COALESCE(t.legacy_id::text,'')
 FROM employee_task t WHERE t.id=$1::uuid AND `+pred, append([]any{id}, args...)...).Scan(&taskID, &scope.WorkspaceID, &scope.AgentID, &scope.TenantOrgID, &scope.Kind, &scope.Scene.SceneID, &scope.LegacyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return employeetask.Task{}, employeetask.ErrNotFound
	}
	if err != nil {
		return employeetask.Task{}, err
	}
	return employeetask.NewStore(tx).Get(ctx, scope, taskID)
}

func (h *Handler) employeeTaskForRead(w http.ResponseWriter, r *http.Request) (employeeTaskReader, pgx.Tx, employeetask.Task, bool) {
	rd, ok := h.employeeTaskReaderFor(w, r)
	if !ok {
		return rd, nil, employeetask.Task{}, false
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "task_id")
	if !ok {
		return rd, nil, employeetask.Task{}, false
	}
	tx, err := h.employeeTaskReadTx(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "employee task storage is unavailable")
		return rd, nil, employeetask.Task{}, false
	}
	task, err := loadReadableEmployeeTask(r.Context(), tx, rd, uuidToString(id))
	if err != nil {
		_ = tx.Rollback(r.Context())
		writeEmployeeTaskReadError(w, err)
		return rd, nil, employeetask.Task{}, false
	}
	return rd, tx, task, true
}

func writeEmployeeTaskReadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, employeetask.ErrNotFound):
		writeError(w, http.StatusNotFound, "employee task not found")
	case errors.Is(err, employeetask.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid employee task request")
	default:
		writeError(w, http.StatusServiceUnavailable, "employee task read failed")
	}
}

// EmployeeTaskView is the Task snapshot. InputSeq is the ledger watermark:
// every accepted input up to it is in the Task's entries.
type EmployeeTaskView struct {
	TaskID           string                  `json:"task_id"`
	WorkspaceID      string                  `json:"workspace_id"`
	AgentID          string                  `json:"agent_id"`
	TenantOrgID      string                  `json:"tenant_org_id"`
	ScopeKind        string                  `json:"scope_kind"`
	SceneID          string                  `json:"scene_id,omitempty"`
	LegacyID         string                  `json:"legacy_id,omitempty"`
	IssueID          string                  `json:"issue_id,omitempty"`
	OwnerLoop        string                  `json:"owner_loop"`
	DispatchMode     string                  `json:"dispatch_mode"`
	RequesterRef     string                  `json:"requester_ref"`
	Definition       employeetask.Definition `json:"definition"`
	GoalRevision     int64                   `json:"goal_revision"`
	Version          int64                   `json:"version"`
	InputSeq         int64                   `json:"input_seq"`
	State            string                  `json:"state"`
	LifecycleVersion int                     `json:"lifecycle_version"`
	CompletionMode   string                  `json:"completion_mode"`
	AutonomousRounds int64                   `json:"autonomous_rounds"`
	ActiveRunID      string                  `json:"active_run_id,omitempty"`
	CreatedAt        time.Time               `json:"created_at"`
	UpdatedAt        time.Time               `json:"updated_at"`
}

func employeeTaskViewOf(t employeetask.Task) EmployeeTaskView {
	def := employeetask.Definition{Goal: employeeTaskData(t.Definition.Goal, 4000)}
	for _, list := range []struct{ in, out *[]string }{{&t.Definition.Deliverables, &def.Deliverables}, {&t.Definition.SuccessCriteria, &def.SuccessCriteria}, {&t.Definition.AccessNeeded, &def.AccessNeeded}} {
		for _, item := range *list.in {
			*list.out = append(*list.out, employeeTaskData(item, 1000))
		}
	}
	mode := t.CompletionMode
	if mode == "" {
		mode = employeetask.CompletionSingleRun
	}
	return EmployeeTaskView{
		TaskID: t.ID, WorkspaceID: t.Scope.WorkspaceID, AgentID: t.Scope.AgentID, TenantOrgID: t.Scope.TenantOrgID, ScopeKind: string(t.Scope.Kind),
		SceneID: t.Scope.Scene.SceneID, LegacyID: t.Scope.LegacyID, IssueID: t.IssueID, OwnerLoop: string(t.OwnerLoop), DispatchMode: string(t.DispatchMode),
		RequesterRef: t.RequesterRef, Definition: def, GoalRevision: t.GoalRevision, Version: t.Version, InputSeq: t.LastEntrySeq, State: string(t.State),
		LifecycleVersion: int(t.Lifecycle()), CompletionMode: string(mode), AutonomousRounds: t.AutonomousRounds, ActiveRunID: t.ActiveRunID,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

// EmployeeTaskRunDelivery is the Employee notice decision for a Run and the
// state of its outbound action. "enqueued" is not delivery; only an action
// state of delivered is provider-confirmed.
type EmployeeTaskRunDelivery struct {
	NoticeState  string `json:"notice_state"`
	NoticeReason string `json:"notice_reason,omitempty"`
	ActionState  string `json:"action_state,omitempty"`
	ReceiptState string `json:"receipt_state,omitempty"`
}

type EmployeeTaskRunView struct {
	RunID        string                     `json:"run_id"`
	QueueTaskID  string                     `json:"queue_task_id"`
	QueueState   string                     `json:"queue_state,omitempty"`
	GoalRevision int64                      `json:"goal_revision"`
	InputSeq     int64                      `json:"input_seq"`
	State        string                     `json:"state"`
	ResultRef    string                     `json:"result_ref,omitempty"`
	ResultReport string                     `json:"result_report,omitempty"`
	CreatedAt    time.Time                  `json:"created_at"`
	FinishedAt   *time.Time                 `json:"finished_at,omitempty"`
	Delivery     *EmployeeTaskRunDelivery   `json:"delivery,omitempty"`
	Verification *employeeverification.Gate `json:"verification,omitempty"`
}

func (h *Handler) employeeTaskRunView(ctx context.Context, tx pgx.Tx, task employeetask.Task, run employeetask.Run) (EmployeeTaskRunView, error) {
	view := EmployeeTaskRunView{RunID: run.ID, QueueTaskID: run.QueueTaskID, GoalRevision: run.GoalRevision, InputSeq: run.InputSeq, State: string(run.State), ResultRef: run.ResultRef, CreatedAt: run.CreatedAt, FinishedAt: run.FinishedAt}
	if run.Result != "" {
		view.ResultReport = employeeTaskData(run.Result, 8000)
	}
	err := tx.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id=$1::uuid`, run.QueueTaskID).Scan(&view.QueueState)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return view, err
	}
	var delivery EmployeeTaskRunDelivery
	err = tx.QueryRow(ctx, `SELECT n.state,n.reason,COALESCE(a.state,''),COALESCE(a.receipt_state,'') FROM employee_run_notice n LEFT JOIN response_action a ON a.id=n.action_id
 WHERE n.workspace_id=$1::uuid AND n.task_id=$2::uuid AND n.run_id=$3::uuid`, task.Scope.WorkspaceID, task.ID, run.ID).Scan(&delivery.NoticeState, &delivery.NoticeReason, &delivery.ActionState, &delivery.ReceiptState)
	switch {
	case err == nil:
		view.Delivery = &delivery
	case !errors.Is(err, pgx.ErrNoRows):
		return view, err
	}
	if task.OwnerLoop == employeetask.LoopEmployee && task.Scope.Kind == employeetask.ScopeScene && run.State == employeetask.StateSucceeded {
		gate, err := employeeverification.GateTx(ctx, tx, task.Scope, task.ID, run.ID)
		switch {
		case err == nil:
			view.Verification = &gate
		case errors.Is(err, employeeverification.ErrNotFound), errors.Is(err, employeeverification.ErrInvalid):
		default:
			return view, err
		}
	}
	return view, nil
}

// EmployeeTaskWaitCounts summarizes the goal's dependencies.
type EmployeeTaskWaitCounts struct {
	OpenMandatory   int `json:"open_mandatory"`
	OpenOptional    int `json:"open_optional"`
	OpenCollections int `json:"open_collections"`
}

type EmployeeTaskDetail struct {
	Task          EmployeeTaskView                  `json:"task"`
	LatestRun     *EmployeeTaskRunView              `json:"latest_run,omitempty"`
	Execution     *service.DirectTaskExecutionState `json:"execution,omitempty"`
	Waits         []employeetask.Wait               `json:"waits"`
	WaitingCounts EmployeeTaskWaitCounts            `json:"waiting_counts"`
	Collections   []taskinput.TaskWait              `json:"collections"`
	Links         employeetask.TaskLinks            `json:"links"`
	EvidenceRefs  []string                          `json:"evidence_refs"`
}

// GetEmployeeTask returns one Task's aggregate projection.
func (h *Handler) GetEmployeeTask(w http.ResponseWriter, r *http.Request) {
	_, tx, task, ok := h.employeeTaskForRead(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	defer tx.Rollback(ctx)
	detail, err := h.employeeTaskDetail(ctx, tx, task)
	if err != nil {
		slog.WarnContext(ctx, "employee task read failed", "task_id", task.ID, "error", err)
		writeEmployeeTaskReadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *Handler) employeeTaskDetail(ctx context.Context, tx pgx.Tx, task employeetask.Task) (EmployeeTaskDetail, error) {
	store := employeetask.NewStore(tx)
	out := EmployeeTaskDetail{Task: employeeTaskViewOf(task), EvidenceRefs: []string{}, Collections: []taskinput.TaskWait{}}
	latest, err := store.LatestRun(ctx, task.Scope, task.ID)
	switch {
	case err == nil:
		view, err := h.employeeTaskRunView(ctx, tx, task, latest)
		if err != nil {
			return out, err
		}
		out.LatestRun = &view
	case !errors.Is(err, employeetask.ErrNotFound):
		return out, err
	}
	if h.TaskService != nil && task.OwnerLoop == employeetask.LoopEmployee && task.DispatchMode == employeetask.DispatchDirect && task.Scope.Kind == employeetask.ScopeScene && task.IssueID == "" {
		execution, err := h.TaskService.ReadDirectTaskExecutionState(ctx, tx, task.Scope, task.ID)
		if err != nil {
			return out, err
		}
		out.Execution = &execution
	}
	if out.Waits, err = store.Waits(ctx, task.Scope, task.ID); err != nil {
		return out, err
	}
	for _, wait := range out.Waits {
		if wait.State == employeetask.WaitOpen {
			if wait.Mandatory {
				out.WaitingCounts.OpenMandatory++
			} else {
				out.WaitingCounts.OpenOptional++
			}
		}
	}
	// Collection counts only; answers stay behind the origin and participant views.
	collections, err := taskinput.NewStore(tx).TaskWaits(ctx, taskinput.Scope{WorkspaceID: task.Scope.WorkspaceID, AgentID: task.Scope.AgentID, TenantOrgID: task.Scope.TenantOrgID}, task.ID)
	if err != nil {
		return out, err
	}
	out.Collections = collections
	for _, c := range collections {
		if c.State == taskinput.CollectionOpen {
			out.WaitingCounts.OpenCollections++
		}
	}
	if out.Links, err = store.Links(ctx, task.Scope, task.ID); err != nil {
		return out, err
	}
	// Evidence: result refs of this revision's successful Runs and the
	// evidence an explicit goal completion recorded.
	rows, err := tx.Query(ctx, `SELECT ref FROM (
 SELECT result_ref AS ref, created_at FROM employee_task_run WHERE workspace_id=$1::uuid AND task_id=$2::uuid AND goal_revision=$3 AND state='succeeded' AND result_ref<>''
 UNION ALL SELECT payload->>'evidence_ref', created_at FROM employee_task_entry WHERE workspace_id=$1::uuid AND task_id=$2::uuid AND kind='goal_completed' AND COALESCE(payload->>'evidence_ref','')<>''
) refs ORDER BY created_at DESC LIMIT 10`, task.Scope.WorkspaceID, task.ID, task.GoalRevision)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var ref string
		if err = rows.Scan(&ref); err != nil {
			return out, err
		}
		if !slices.Contains(out.EvidenceRefs, ref) {
			out.EvidenceRefs = append(out.EvidenceRefs, employeeTaskData(ref, 500))
		}
	}
	return out, rows.Err()
}

type employeeTaskCursor struct {
	UpdatedAt time.Time `json:"u"`
	ID        string    `json:"i"`
}

func queryLimit(r *http.Request, def, maximum int) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return def, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maximum {
		return 0, false
	}
	return n, true
}

// ListEmployeeTasks pages the Tasks the caller may read, newest update first.
func (h *Handler) ListEmployeeTasks(w http.ResponseWriter, r *http.Request) {
	rd, ok := h.employeeTaskReaderFor(w, r)
	if !ok {
		return
	}
	limit, ok := queryLimit(r, employeeTaskListDefault, employeeTaskListMax)
	if !ok {
		writeError(w, http.StatusBadRequest, "limit must be between 1 and 50")
		return
	}
	pred, args := rd.visible(1)
	query := `SELECT t.id::text,t.workspace_id::text,t.agent_id::text,t.tenant_org_id,t.scope_kind,COALESCE(t.scene_id::text,''),COALESCE(t.legacy_id::text,''),t.updated_at FROM employee_task t WHERE ` + pred
	if agent := r.URL.Query().Get("agent_id"); agent != "" {
		id, ok := parseUUIDOrBadRequest(w, agent, "agent_id")
		if !ok {
			return
		}
		args = append(args, uuidToString(id))
		query += ` AND t.agent_id=$` + strconv.Itoa(len(args)) + `::uuid`
	}
	if state := r.URL.Query().Get("state"); state != "" {
		if !slices.Contains(employeetask.AllStates(), employeetask.State(state)) {
			writeError(w, http.StatusBadRequest, "unknown state")
			return
		}
		args = append(args, state)
		query += ` AND t.state=$` + strconv.Itoa(len(args))
	}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var cursor employeeTaskCursor
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.UpdatedAt.IsZero() || !validUUIDString(cursor.ID) {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		args = append(args, cursor.UpdatedAt, cursor.ID)
		query += ` AND (t.updated_at, t.id) < ($` + strconv.Itoa(len(args)-1) + `, $` + strconv.Itoa(len(args)) + `::uuid)`
	}
	args = append(args, limit+1)
	query += ` ORDER BY t.updated_at DESC, t.id DESC LIMIT $` + strconv.Itoa(len(args))
	ctx := r.Context()
	tx, err := h.employeeTaskReadTx(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "employee task storage is unavailable")
		return
	}
	defer tx.Rollback(ctx)
	type row struct {
		scope   employeetask.Scope
		id      string
		updated time.Time
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		writeEmployeeTaskReadError(w, err)
		return
	}
	var found []row
	for rows.Next() {
		var x row
		if err = rows.Scan(&x.id, &x.scope.WorkspaceID, &x.scope.AgentID, &x.scope.TenantOrgID, &x.scope.Kind, &x.scope.Scene.SceneID, &x.scope.LegacyID, &x.updated); err != nil {
			rows.Close()
			writeEmployeeTaskReadError(w, err)
			return
		}
		found = append(found, x)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		writeEmployeeTaskReadError(w, err)
		return
	}
	out := struct {
		Tasks      []EmployeeTaskView `json:"tasks"`
		NextCursor string             `json:"next_cursor,omitempty"`
	}{Tasks: []EmployeeTaskView{}}
	store := employeetask.NewStore(tx)
	for i, x := range found {
		if i == limit {
			raw, _ := json.Marshal(employeeTaskCursor{UpdatedAt: found[i-1].updated, ID: found[i-1].id})
			out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
			break
		}
		task, err := store.Get(ctx, x.scope, x.id)
		if err != nil {
			writeEmployeeTaskReadError(w, err)
			return
		}
		out.Tasks = append(out.Tasks, employeeTaskViewOf(task))
	}
	writeJSON(w, http.StatusOK, out)
}

// EmployeeTaskEntryView is one ledger entry; its payload is never exposed.
type EmployeeTaskEntryView struct {
	Seq             int64     `json:"seq"`
	Kind            string    `json:"kind"`
	SourceNamespace string    `json:"source_namespace"`
	ActorRef        string    `json:"actor_ref,omitempty"`
	GoalRevision    int64     `json:"goal_revision"`
	RunID           string    `json:"run_id,omitempty"`
	Body            string    `json:"body,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// ListEmployeeTaskEntries pages the append-only ledger by seq.
func (h *Handler) ListEmployeeTaskEntries(w http.ResponseWriter, r *http.Request) {
	after := int64(0)
	if raw := r.URL.Query().Get("after_seq"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "after_seq must be a non-negative integer")
			return
		}
		after = n
	}
	limit, ok := queryLimit(r, employeeTaskEntryDefault, employeeTaskEntryMax)
	if !ok {
		writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
		return
	}
	_, tx, task, ok := h.employeeTaskForRead(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	defer tx.Rollback(ctx)
	entries, err := employeetask.NewStore(tx).ReadEntries(ctx, task.Scope, task.ID, after, limit)
	if err != nil {
		writeEmployeeTaskReadError(w, err)
		return
	}
	out := struct {
		TaskID       string                  `json:"task_id"`
		InputSeq     int64                   `json:"input_seq"`
		Entries      []EmployeeTaskEntryView `json:"entries"`
		NextAfterSeq *int64                  `json:"next_after_seq,omitempty"`
	}{TaskID: task.ID, InputSeq: task.LastEntrySeq, Entries: []EmployeeTaskEntryView{}}
	for _, e := range entries {
		out.Entries = append(out.Entries, EmployeeTaskEntryView{Seq: e.Seq, Kind: e.Kind, SourceNamespace: e.Source.Namespace, ActorRef: e.ActorRef, GoalRevision: e.GoalRevision, RunID: e.RunID, Body: employeeTaskData(e.Body, 2000), CreatedAt: e.CreatedAt})
	}
	if n := len(entries); n == limit && entries[n-1].Seq < task.LastEntrySeq {
		next := entries[n-1].Seq
		out.NextAfterSeq = &next
	}
	writeJSON(w, http.StatusOK, out)
}

// ListEmployeeTaskRuns pages a Task's Runs in creation order.
func (h *Handler) ListEmployeeTaskRuns(w http.ResponseWriter, r *http.Request) {
	afterID := r.URL.Query().Get("after_run_id")
	if afterID != "" && !validUUIDString(afterID) {
		writeError(w, http.StatusBadRequest, "invalid after_run_id")
		return
	}
	limit, ok := queryLimit(r, employeeTaskRunDefault, employeeTaskRunMax)
	if !ok {
		writeError(w, http.StatusBadRequest, "limit must be between 1 and 50")
		return
	}
	_, tx, task, ok := h.employeeTaskForRead(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	defer tx.Rollback(ctx)
	runs, err := employeetask.NewStore(tx).ListRuns(ctx, task.Scope, task.ID, afterID, limit+1)
	if err != nil {
		writeEmployeeTaskReadError(w, err)
		return
	}
	out := struct {
		TaskID         string                `json:"task_id"`
		Runs           []EmployeeTaskRunView `json:"runs"`
		NextAfterRunID string                `json:"next_after_run_id,omitempty"`
	}{TaskID: task.ID, Runs: []EmployeeTaskRunView{}}
	for i, run := range runs {
		if i == limit {
			out.NextAfterRunID = runs[i-1].ID
			break
		}
		view, err := h.employeeTaskRunView(ctx, tx, task, run)
		if err != nil {
			writeEmployeeTaskReadError(w, err)
			return
		}
		out.Runs = append(out.Runs, view)
	}
	writeJSON(w, http.StatusOK, out)
}
