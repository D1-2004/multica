package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// EmployeeTaskSteerMaxBytes bounds one correction. Every successor renders all
// recorded corrections, so the bound also bounds the successor packet.
const (
	EmployeeTaskSteerMaxBytes   = 16000
	employeeTaskSteerRenderMax  = 20
	employeeTaskSteerRunSuffix  = "/run"
	employeeTaskSteerFenceSpace = "steer_writer_fence"
)

var ErrEmployeeTaskSteerUnsupported = errors.New("employee task backend does not support steer")

// EmployeeTaskSteerRequest is host-authorized input. The host has verified the
// caller may control this task; ActorRef is recorded verbatim in the ledger.
type EmployeeTaskSteerRequest struct {
	Task     employeetask.Task
	Source   employeetask.Source
	ActorRef string
	Content  string
	// AuthorID is the authenticated member when a human issued the correction.
	// The Issue backend requires it to author the member comment.
	AuthorID pgtype.UUID
	// Context is optional host-owned dispatch context for the correction's own
	// launch identity (for example a fresh identity token). Direct ownership
	// keys and the scene reference cannot be overridden through it.
	Context json.RawMessage
	// SameRequester is the host's verified claim that the correction comes from
	// the task's original requester. Otherwise the successor runs without the
	// requester's personal connectors; identity tokens never carry over.
	SameRequester bool
}

type EmployeeTaskSteerResult struct {
	Outcome     employeetask.SteerOutcome
	Task        employeetask.Task
	Entry       employeetask.Entry
	Run         employeetask.Run
	Queue       db.AgentTaskQueue
	Interrupted *db.AgentTaskQueue
	CommentID   pgtype.UUID
	Replayed    bool
}

// EmployeeTaskControl is the Task Service control surface: it routes a
// correction to the backend that owns the task's executions.
type EmployeeTaskControl struct {
	Tasks  *TaskService
	Issues *EmployeeIssueBackend
}

// Steer interrupts the task's current execution and resumes it with the
// correction as the next input. See docs/task-steer.md.
func (c EmployeeTaskControl) Steer(ctx context.Context, req EmployeeTaskSteerRequest) (EmployeeTaskSteerResult, error) {
	if c.Tasks == nil || c.Tasks.TxStarter == nil {
		return EmployeeTaskSteerResult{}, errors.New("employee task steer requires a transaction starter")
	}
	if err := validateEmployeeTaskSteer(req); err != nil {
		return EmployeeTaskSteerResult{}, err
	}
	task, err := c.Tasks.readEmployeeTask(ctx, req.Task.Scope, req.Task.ID)
	if err != nil {
		return EmployeeTaskSteerResult{}, err
	}
	if !employeetask.Capabilities(task.DispatchMode).Steer {
		return EmployeeTaskSteerResult{}, ErrEmployeeTaskSteerUnsupported
	}
	switch task.DispatchMode {
	case employeetask.DispatchDirect:
		return c.Tasks.steerDirectEmployeeTask(ctx, req)
	case employeetask.DispatchIssue:
		return c.steerIssueEmployeeTask(ctx, task, req)
	default:
		return EmployeeTaskSteerResult{}, ErrEmployeeTaskSteerUnsupported
	}
}

func validateEmployeeTaskSteer(req EmployeeTaskSteerRequest) error {
	content := strings.TrimSpace(req.Content)
	if content == "" || len(content) > EmployeeTaskSteerMaxBytes || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return employeetask.ErrInvalid
	}
	if strings.TrimSpace(req.ActorRef) == "" || req.Source.Namespace == "" || req.Source.Key == "" || strings.HasSuffix(req.Source.Key, employeeTaskSteerRunSuffix) {
		return employeetask.ErrInvalid
	}
	return nil
}

// steerIssueEmployeeTask reuses the Issue steer primitive: one member comment
// plus one successor, coalesced and exit-fenced by the existing Issue contract.
func (c EmployeeTaskControl) steerIssueEmployeeTask(ctx context.Context, task employeetask.Task, req EmployeeTaskSteerRequest) (EmployeeTaskSteerResult, error) {
	if c.Issues == nil || c.Issues.Issues == nil || !req.AuthorID.Valid || task.IssueID == "" {
		return EmployeeTaskSteerResult{}, ErrEmployeeTaskSteerUnsupported
	}
	issueID, err := util.ParseUUID(task.IssueID)
	if err != nil {
		return EmployeeTaskSteerResult{}, err
	}
	issue, err := c.Issues.Issues.Queries.GetIssue(ctx, issueID)
	if err != nil {
		return EmployeeTaskSteerResult{}, err
	}
	if util.UUIDToString(issue.WorkspaceID) != task.Scope.WorkspaceID || util.UUIDToString(issue.AssigneeID) != task.Scope.AgentID || issue.AssigneeType.String != "agent" {
		return EmployeeTaskSteerResult{}, employeetask.ErrConflict
	}
	key := req.Source.Namespace + ":" + req.Source.Key
	if len(key) > 256 {
		return EmployeeTaskSteerResult{}, employeetask.ErrInvalid
	}
	started := time.Now()
	result, err := c.Issues.Continue(ctx, EmployeeIssueContinueParams{
		Comment:  IssueCommentCreateParams{Issue: issue, AuthorID: req.AuthorID, Content: strings.TrimSpace(req.Content), QueueMode: "steer", IdempotencyKey: key},
		ActorRef: req.ActorRef,
	})
	if err != nil {
		return EmployeeTaskSteerResult{}, err
	}
	out := EmployeeTaskSteerResult{Queue: result.Task, Interrupted: result.PreemptedTask, CommentID: result.Comment.ID, Outcome: employeetask.SteerContinued}
	switch {
	case result.PreemptedTask != nil:
		out.Outcome = employeetask.SteerInterrupted
	case result.Task.CreatedAt.Valid && result.Task.CreatedAt.Time.Before(started):
		out.Outcome = employeetask.SteerMerged
	}
	if out.Task, err = c.Tasks.readEmployeeTask(ctx, task.Scope, task.ID); err != nil {
		return out, err
	}
	tx, err := c.Tasks.TxStarter.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if run, err := directRunByQueue(ctx, tx, result.Task.ID); err == nil && run.TaskID == task.ID {
		out.Run = run
	}
	return out, nil
}

func (s *TaskService) readEmployeeTask(ctx context.Context, scope employeetask.Scope, id string) (employeetask.Task, error) {
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return employeetask.Task{}, err
	}
	defer tx.Rollback(ctx)
	return employeetask.NewStore(tx).Get(ctx, scope, id)
}

// steerDirectEmployeeTask is the Direct backend. One transaction cancels a
// claimed Run with the exit barrier, records the correction and the writer
// fence, and queues one successor that resumes the predecessor's session.
// Corrections arriving before that successor is claimed join it.
func (s *TaskService) steerDirectEmployeeTask(ctx context.Context, req EmployeeTaskSteerRequest) (EmployeeTaskSteerResult, error) {
	var out EmployeeTaskSteerResult
	agentID, err := util.ParseUUID(req.Task.Scope.AgentID)
	if err != nil {
		return out, employeetask.ErrInvalid
	}
	// Personal connector resolution may call external services, so it runs
	// before any aggregate lock, like EnqueueDirectTask. A terminal row has its
	// overlay cleared, so the successor's overlay is always recomputed.
	overlay := s.directSteerOverlay(ctx, req, agentID)
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	// Lock order matches the domain and teardown: workspace -> task, then the
	// agent claim lock so no runtime claims a row between these transitions.
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, req.Task.Scope.WorkspaceID).Scan(&locked); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `SELECT id::text FROM employee_task WHERE id=$1::uuid AND workspace_id=$2::uuid FOR UPDATE`, req.Task.ID, req.Task.Scope.WorkspaceID).Scan(&locked); err != nil {
		return out, mapEmployeeTaskSteerError(err)
	}
	qtx := s.Queries.WithTx(tx)
	agent, err := qtx.GetAgentForClaimUpdate(ctx, agentID)
	if err != nil {
		return out, err
	}
	store := employeetask.NewStore(tx)
	task, err := store.Get(ctx, req.Task.Scope, req.Task.ID)
	if err != nil {
		return out, err
	}
	if task.DispatchMode != employeetask.DispatchDirect {
		return out, employeetask.ErrConflict
	}
	content := strings.TrimSpace(req.Content)
	if entry, err := store.EntryBySource(ctx, task.Scope, task.ID, req.Source); err == nil {
		return s.replayDirectEmployeeTaskSteer(ctx, tx, store, task, entry, content, req.ActorRef)
	} else if !errors.Is(err, employeetask.ErrNotFound) {
		return out, err
	}
	latest, err := store.LatestRun(ctx, task.Scope, task.ID)
	if errors.Is(err, employeetask.ErrNotFound) {
		// Nothing has executed, so there is no session or input to resume.
		return out, employeetask.ErrConflict
	}
	if err != nil {
		return out, err
	}
	latestQueue, err := lockEmployeeSteerQueue(ctx, tx, latest.QueueTaskID)
	if err != nil {
		return out, err
	}
	// A Run whose queue row already reached a terminal state but was not yet
	// reconciled is settled first; it is history, not an active writer.
	if task.ActiveRunID == latest.ID && fcE2BTaskIsTerminal(latestQueue.Status) {
		if err = s.recordEmployeeRunInTx(ctx, tx, latestQueue, latestQueue.Status, latestQueue.Result, latestQueue.Error.String); err != nil {
			return out, err
		}
		if task, err = store.Get(ctx, task.Scope, task.ID); err != nil {
			return out, err
		}
		if latest, err = store.LatestRun(ctx, task.Scope, task.ID); err != nil {
			return out, err
		}
	}
	steer := employeetask.SteerParams{Source: req.Source, ActorRef: req.ActorRef, Body: content}
	if task.ActiveRunID != "" && task.ActiveRunID != latest.ID {
		return out, employeetask.ErrConflict
	}
	if task.ActiveRunID != "" && (latestQueue.Status == "queued" || latestQueue.Status == "deferred") {
		// The active Run has not been claimed: the correction joins it.
		steer.MergeRunID = latest.ID
		if out.Task, out.Entry, err = store.Steer(ctx, task.Scope, task.ID, steer); err != nil {
			return out, err
		}
		next, err := directSteerSuccessorContext(latestQueue, req.Context, latestQueue.ID, s.steerCorrections(ctx, store, task))
		if err != nil {
			return out, err
		}
		// A correction is the newest human input; keep the predecessor link of
		// an earlier steer successor so its session lookup is unchanged.
		next, err = keepSteerPredecessor(next, latestQueue)
		if err != nil {
			return out, err
		}
		if _, err = tx.Exec(ctx, `UPDATE agent_task_queue SET context=$2, priority=GREATEST(priority,4),
 runtime_mcp_overlay=CASE WHEN $3 THEN runtime_mcp_overlay END, runtime_connected_apps=CASE WHEN $3 THEN runtime_connected_apps END
 WHERE id=$1 AND status IN ('queued','deferred')`, latestQueue.ID, next, req.SameRequester); err != nil {
			return out, err
		}
		if out.Queue, err = qtx.GetAgentTask(ctx, latestQueue.ID); err != nil {
			return out, err
		}
		out.Run, err = store.LatestRun(ctx, task.Scope, task.ID)
		if err != nil {
			return out, err
		}
		out.Outcome = employeetask.SteerMerged
		if err = tx.Commit(ctx); err != nil {
			return EmployeeTaskSteerResult{}, err
		}
		// The row was already enqueued once; wake it without recounting it.
		s.wakeQueuedEmployeeSteer(ctx, out.Queue)
		return out, nil
	}
	// Admission is checked before anything is cancelled: a correction that
	// cannot run must not stop the current execution.
	if err = directSteerAdmission(ctx, qtx, agent); err != nil {
		return out, err
	}
	out.Outcome = employeetask.SteerContinued
	if task.ActiveRunID != "" {
		cancelled, err := qtx.CancelAgentTaskForSteer(ctx, latestQueue.ID)
		if err != nil {
			return out, fmt.Errorf("cancel steered execution: %w", err)
		}
		out.Interrupted = &cancelled
		out.Outcome = employeetask.SteerInterrupted
		if err = s.recordEmployeeRunInTx(ctx, tx, cancelled, "cancelled", nil, ""); err != nil {
			return out, err
		}
		if _, err = enqueueSteerCallbackCompletions(ctx, qtx, cancelled, "canceled", nil, "", ""); err != nil {
			return out, err
		}
		latestQueue = cancelled
		if latest, err = store.LatestRun(ctx, task.Scope, task.ID); err != nil {
			return out, err
		}
	}
	if latest.State == employeetask.StateFailed || latest.State == employeetask.StateCancelled {
		if evidence := directWriterFenceEvidence(latestQueue); evidence != "" {
			if _, _, err = store.FenceRunWriter(ctx, task.Scope, task.ID, employeetask.FenceWriterParams{Source: employeetask.Source{Namespace: employeeTaskSteerFenceSpace, Key: latest.ID + "/" + evidence}, RunID: latest.ID, Evidence: evidence}); err != nil {
				return out, err
			}
		}
	}
	if task, out.Entry, err = store.Steer(ctx, task.Scope, task.ID, steer); err != nil {
		return out, err
	}
	next, err := directSteerSuccessorContext(latestQueue, req.Context, latestQueue.ID, s.steerCorrections(ctx, store, task))
	if err != nil {
		return out, err
	}
	queueID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	if _, err = tx.Exec(ctx, `INSERT INTO agent_task_queue(id,agent_id,runtime_id,status,priority,context,originator_user_id,accountable_user_id,originator_source,trigger_evidence_kind,trigger_evidence_ref_id,trigger_summary,runtime_mcp_overlay,runtime_connected_apps,max_attempts)
 SELECT $1,p.agent_id,$3,'queued',4,$4,p.originator_user_id,p.accountable_user_id,p.originator_source,p.trigger_evidence_kind,p.trigger_evidence_ref_id,p.trigger_summary,$5,$6,1 FROM agent_task_queue p WHERE p.id=$2`, queueID, latestQueue.ID, agent.RuntimeID, next, overlay.Overlay, overlay.ConnectedApps); err != nil {
		return out, err
	}
	if out.Run, err = store.StartRun(ctx, task.Scope, task.ID, employeetask.StartRunParams{Source: employeetask.Source{Namespace: req.Source.Namespace, Key: req.Source.Key + employeeTaskSteerRunSuffix}, QueueTaskID: util.UUIDToString(queueID), ExpectedVersion: task.Version}); err != nil {
		return out, err
	}
	if out.Queue, err = qtx.GetAgentTask(ctx, queueID); err != nil {
		return out, err
	}
	if out.Task, err = store.Get(ctx, task.Scope, task.ID); err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return EmployeeTaskSteerResult{}, err
	}
	if out.Interrupted != nil {
		s.captureTaskCancelled(ctx, *out.Interrupted)
		s.ReconcileAgentStatus(ctx, out.Interrupted.AgentID)
		s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, *out.Interrupted)
		s.NotifyTaskFinished(*out.Interrupted)
		if s.CompletionNotifier != nil {
			s.CompletionNotifier.NotifyTaskCompletion()
		}
	}
	// The successor is claimable only after the barrier opens; the claim SQL and
	// cloud launch arbitration both enforce it, so waking early is harmless.
	s.NotifyTaskEnqueued(ctx, out.Queue)
	return out, nil
}

func (s *TaskService) replayDirectEmployeeTaskSteer(ctx context.Context, tx pgx.Tx, store *employeetask.Store, task employeetask.Task, entry employeetask.Entry, content, actor string) (EmployeeTaskSteerResult, error) {
	out := EmployeeTaskSteerResult{Task: task, Entry: entry, Replayed: true}
	if entry.Kind != "steer" || entry.Body != content || entry.ActorRef != actor {
		return out, employeetask.ErrConflict
	}
	var err error
	if entry.RunID != "" {
		out.Outcome = employeetask.SteerMerged
		out.Run, err = directRunByID(ctx, tx, task, entry.RunID)
	} else {
		out.Outcome = employeetask.SteerContinued
		out.Run, err = store.RunBySource(ctx, task.Scope, task.ID, employeetask.Source{Namespace: entry.Source.Namespace, Key: entry.Source.Key + employeeTaskSteerRunSuffix})
	}
	if err != nil {
		return out, err
	}
	queueID, err := util.ParseUUID(out.Run.QueueTaskID)
	if err != nil {
		return out, err
	}
	if out.Queue, err = s.Queries.WithTx(tx).GetAgentTask(ctx, queueID); err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return EmployeeTaskSteerResult{}, err
	}
	// Recover a commit-before-notify crash; wakeups are idempotent.
	s.wakeQueuedEmployeeSteer(ctx, out.Queue)
	return out, nil
}

// wakeQueuedEmployeeSteer wakes an already-counted queued row. Like a Direct
// replay, it never emits the queued analytics event a second time.
func (s *TaskService) wakeQueuedEmployeeSteer(ctx context.Context, q db.AgentTaskQueue) {
	if q.Status != "queued" {
		return
	}
	s.notifyTaskAvailable(q)
	s.launchRuntimeForTaskWithContext(ctx, q)
}

func directRunByID(ctx context.Context, tx pgx.Tx, task employeetask.Task, runID string) (employeetask.Run, error) {
	var r employeetask.Run
	err := tx.QueryRow(ctx, `SELECT id::text,task_id::text,queue_task_id::text,goal_revision,input_seq,state,result,result_ref,created_at,finished_at FROM employee_task_run WHERE id=$1::uuid AND task_id=$2::uuid AND workspace_id=$3::uuid`, runID, task.ID, task.Scope.WorkspaceID).Scan(&r.ID, &r.TaskID, &r.QueueTaskID, &r.GoalRevision, &r.InputSeq, &r.State, &r.Result, &r.ResultRef, &r.CreatedAt, &r.FinishedAt)
	return r, mapEmployeeTaskSteerError(err)
}

func mapEmployeeTaskSteerError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return employeetask.ErrNotFound
	}
	return err
}

func lockEmployeeSteerQueue(ctx context.Context, tx pgx.Tx, queueTaskID string) (db.AgentTaskQueue, error) {
	id, err := util.ParseUUID(queueTaskID)
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	// Hold the row so a concurrent claim (FOR UPDATE SKIP LOCKED) cannot move
	// it between the status read and the merge or cancellation below.
	var locked pgtype.UUID
	if err = tx.QueryRow(ctx, `SELECT id FROM agent_task_queue WHERE id=$1 FOR UPDATE`, id).Scan(&locked); err != nil {
		return db.AgentTaskQueue{}, mapEmployeeTaskSteerError(err)
	}
	return db.New(tx).GetAgentTask(ctx, id)
}

func directSteerAdmission(ctx context.Context, qtx *db.Queries, agent db.Agent) error {
	ready, reason, err := AgentReadiness(ctx, qtx, agent)
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("direct task agent unavailable: %s", reason)
	}
	runtime, err := qtx.GetAgentRuntime(ctx, agent.RuntimeID)
	if err != nil {
		return err
	}
	if runtime.WorkspaceID != agent.WorkspaceID {
		return ErrDirectTaskAccessDenied
	}
	if !DirectTaskRuntimeCapable(runtime) {
		return fmt.Errorf("direct task runtime lacks %s", protocol.DaemonCapabilityEmployeeDirectV1)
	}
	return nil
}

// directWriterFenceEvidence reads what the queue row proves about its writer.
// A failed row or an old cancellation without the barrier proves nothing.
func directWriterFenceEvidence(q db.AgentTaskQueue) string {
	if q.Status != "cancelled" {
		return ""
	}
	var private struct {
		Pending   bool   `json:"process_stop_pending"`
		StoppedAt string `json:"process_stopped_at"`
	}
	_ = json.Unmarshal(q.Context, &private)
	switch {
	case private.StoppedAt != "":
		return employeetask.FenceProcessStopped
	case private.Pending:
		return employeetask.FenceClaimBarrier
	case !q.DispatchedAt.Valid && !q.StartedAt.Valid:
		return employeetask.FenceNeverClaimed
	default:
		return ""
	}
}

func (s *TaskService) steerCorrections(ctx context.Context, store *employeetask.Store, task employeetask.Task) []employeetask.SteerCorrection {
	entries, err := store.Corrections(ctx, task.Scope, task.ID, employeeTaskSteerRenderMax)
	if err != nil {
		return nil
	}
	out := make([]employeetask.SteerCorrection, 0, len(entries))
	for _, e := range entries {
		out = append(out, employeetask.SteerCorrection{Ref: fmt.Sprintf("employee_task_entry:%s/%d", e.TaskID, e.Seq), ActorRef: e.ActorRef, Body: e.Body})
	}
	return out
}

// directSteerContextOwned lists keys the Host stamps for a Direct execution;
// a correction's dispatch context cannot replace them.
var directSteerContextOwned = map[string]bool{
	"type": true, "workspace_id": true, "employee_task_id": true, "direct_task_prompt": true,
	"direct_principal_id": true, "direct_originator_user_id": true, "employee_direct_input": true,
	"direct_steer_base_prompt": true, "task_steer": true, "steer_predecessor_task_id": true,
	protocol.AgentSceneContextKey: true,
}

// directSteerSuccessorContext rebuilds a successor from the predecessor's
// frozen Host input, never from runtime-enriched top-level state such as
// barrier markers, delivery flags or an earlier author's credentials.
func directSteerSuccessorContext(predecessor db.AgentTaskQueue, overlay json.RawMessage, predecessorID pgtype.UUID, corrections []employeetask.SteerCorrection) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(predecessor.Context, &top); err != nil || top == nil {
		return nil, employeetask.ErrInvalid
	}
	raw, ok := top["employee_direct_input"]
	if !ok {
		return nil, employeetask.ErrInvalid
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(raw, &input); err != nil || input == nil {
		return nil, employeetask.ErrInvalid
	}
	next := make(map[string]json.RawMessage, len(input)+6)
	for k, v := range input {
		next[k] = v
	}
	if len(overlay) > 0 {
		var extra map[string]json.RawMessage
		if err := json.Unmarshal(overlay, &extra); err != nil {
			return nil, employeetask.ErrInvalid
		}
		for k, v := range extra {
			if !directSteerContextOwned[k] {
				next[k] = v
			}
		}
	}
	base := top["direct_steer_base_prompt"]
	if len(base) == 0 {
		base = input["direct_task_prompt"]
	}
	var basePrompt string
	if err := json.Unmarshal(base, &basePrompt); err != nil || strings.TrimSpace(basePrompt) == "" {
		return nil, employeetask.ErrInvalid
	}
	next["direct_steer_base_prompt"], _ = json.Marshal(basePrompt)
	next["direct_task_prompt"], _ = json.Marshal(employeetask.WithCorrections(basePrompt, corrections))
	next["employee_direct_input"] = raw
	next["task_steer"] = json.RawMessage("true")
	next["steer_predecessor_task_id"], _ = json.Marshal(util.UUIDToString(predecessorID))
	return json.Marshal(next)
}

// keepSteerPredecessor preserves the predecessor link of a merged successor.
func keepSteerPredecessor(next []byte, queued db.AgentTaskQueue) ([]byte, error) {
	var current struct {
		Predecessor string `json:"steer_predecessor_task_id"`
	}
	_ = json.Unmarshal(queued.Context, &current)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(next, &fields); err != nil {
		return nil, err
	}
	if current.Predecessor != "" {
		fields["steer_predecessor_task_id"], _ = json.Marshal(current.Predecessor)
	} else {
		// The original Run absorbs a correction before its first claim; it has
		// no predecessor and is not a steer successor.
		delete(fields, "steer_predecessor_task_id")
		delete(fields, "task_steer")
	}
	return json.Marshal(fields)
}

// directSteerOverlay recomputes the requester's personal connectors only when
// the correction comes from that requester; anyone else gets none.
func (s *TaskService) directSteerOverlay(ctx context.Context, req EmployeeTaskSteerRequest, agentID pgtype.UUID) runtimeMCPOverlayData {
	if !req.SameRequester || s.Composio == nil {
		return runtimeMCPOverlayData{}
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return runtimeMCPOverlayData{}
	}
	defer tx.Rollback(ctx)
	var originator pgtype.UUID
	err = tx.QueryRow(ctx, `SELECT q.originator_user_id FROM employee_task_run r JOIN agent_task_queue q ON q.id=r.queue_task_id WHERE r.task_id=$1::uuid AND r.workspace_id=$2::uuid ORDER BY r.created_at DESC, r.id DESC LIMIT 1`, req.Task.ID, req.Task.Scope.WorkspaceID).Scan(&originator)
	if err != nil || !originator.Valid {
		return runtimeMCPOverlayData{}
	}
	agent, err := s.Queries.GetAgent(ctx, agentID)
	_ = tx.Rollback(ctx)
	if err != nil {
		return runtimeMCPOverlayData{}
	}
	return s.buildRuntimeMCPOverlay(ctx, originator, agent)
}
