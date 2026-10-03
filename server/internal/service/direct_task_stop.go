package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type DirectTaskStopRequest struct {
	Task               employeetask.Task
	RunID, QueueTaskID string
	PrincipalID        pgtype.UUID
	Source             employeetask.Source
	ActorRef, Body     string
}

type DirectTaskStopResult struct {
	Task               employeetask.Task
	Run                employeetask.Run
	Queue              db.AgentTaskQueue
	Entry              employeetask.Entry
	State              string
	Changed            bool
	QueueChanged       bool
	Replayed           bool
	ExitConfirmed      bool
	PendingPredecessor bool
	pending            []db.AgentTaskQueue
}

// DirectTaskExecutionState contains no task input, report or source message.
// State describes execution evidence, independently of the closed goal state.
type DirectTaskExecutionState struct {
	TaskID             string             `json:"task_id"`
	RunID              string             `json:"run_id,omitempty"`
	QueueTaskID        string             `json:"queue_task_id,omitempty"`
	TaskState          employeetask.State `json:"task_state"`
	QueueState         string             `json:"queue_state,omitempty"`
	State              string             `json:"state"`
	StopRequested      bool               `json:"stop_requested"`
	ExitConfirmed      bool               `json:"exit_confirmed"`
	PendingPredecessor bool               `json:"pending_predecessor"`
}

// StopDirectTaskTx uses a savepoint because its caller may journal a rejected
// tool call and commit the outer transaction. No notification occurs here.
func (s *TaskService) StopDirectTaskTx(ctx context.Context, outer pgx.Tx, req DirectTaskStopRequest) (DirectTaskStopResult, error) {
	var out DirectTaskStopResult
	if s == nil || s.Queries == nil || outer == nil || !req.PrincipalID.Valid || req.Task.Version <= 0 || strings.TrimSpace(req.ActorRef) == "" || strings.TrimSpace(req.Body) == "" || (req.RunID == "") != (req.QueueTaskID == "") {
		return out, employeetask.ErrInvalid
	}
	for _, id := range []string{req.Task.ID, req.Task.Scope.WorkspaceID, req.Task.Scope.AgentID} {
		if _, err := util.ParseUUID(id); err != nil {
			return out, employeetask.ErrInvalid
		}
	}
	tx, err := outer.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	// Completion and cancellation both acquire workspace -> queue -> Task.
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM workspace WHERE id=$1::uuid FOR KEY SHARE`, req.Task.Scope.WorkspaceID).Scan(&locked); err != nil {
		return out, err
	}
	if req.QueueTaskID != "" {
		if _, err := util.ParseUUID(req.RunID); err != nil {
			return out, employeetask.ErrInvalid
		}
		out.Queue, err = lockEmployeeSteerQueue(ctx, tx, req.QueueTaskID)
		if err != nil {
			return out, err
		}
	}
	if err = tx.QueryRow(ctx, `SELECT id::text FROM employee_task WHERE id=$1::uuid AND workspace_id=$2::uuid AND agent_id=$3::uuid AND tenant_org_id=$4 FOR UPDATE`, req.Task.ID, req.Task.Scope.WorkspaceID, req.Task.Scope.AgentID, req.Task.Scope.TenantOrgID).Scan(&locked); err != nil {
		return out, mapEmployeeTaskSteerError(err)
	}
	store, q := employeetask.NewStore(tx), s.Queries.WithTx(tx)
	task, err := store.Get(ctx, req.Task.Scope, req.Task.ID)
	if err != nil {
		return out, err
	}
	if task.OwnerLoop != employeetask.LoopEmployee || task.DispatchMode != employeetask.DispatchDirect || task.Scope.Kind != employeetask.ScopeScene || task.IssueID != "" || task.RequesterRef != req.ActorRef || task.RequesterRef != req.Task.RequesterRef {
		return out, ErrDirectTaskAccessDenied
	}
	agent, err := directTaskAdmissionAgent(ctx, q, DirectTaskRequest{Task: task, PrincipalID: req.PrincipalID})
	if err != nil {
		return out, err
	}
	if agent.ArchivedAt.Valid {
		return out, ErrDirectTaskAccessDenied
	}
	if req.RunID != "" {
		out.Run, err = directStopRun(ctx, tx, task, req.RunID, req.QueueTaskID)
		if err != nil {
			return out, err
		}
		if !directStopQueueMatches(task, out.Queue) {
			return out, ErrDirectTaskAccessDenied
		}
	}
	_, err = store.EntryBySource(ctx, task.Scope, task.ID, req.Source)
	if err == nil {
		out.Replayed = true
	} else if !errors.Is(err, employeetask.ErrNotFound) {
		return out, err
	}
	params := employeetask.StopParams{Source: req.Source, ActorRef: req.ActorRef, Body: req.Body, RunID: req.RunID, QueueTaskID: req.QueueTaskID, ExpectedVersion: req.Task.Version}
	out.Task, out.Entry, err = store.Stop(ctx, task.Scope, task.ID, params)
	if err != nil {
		return out, err
	}
	// A stopped goal stops collecting: its collections close in this transaction.
	if err = closeTaskCollectionsTx(ctx, tx, out.Task, req.Source); err != nil {
		return out, err
	}
	out.Changed = !out.Replayed
	if !out.Replayed && req.RunID != "" {
		switch out.Queue.Status {
		case "queued", "deferred", "dispatched", "running", "waiting_local_directory":
			if out.Run.State != employeetask.StateRunning {
				return out, employeetask.ErrConflict
			}
			out.Queue, err = q.CancelAgentTaskForSteer(ctx, out.Queue.ID)
			if err != nil {
				return out, err
			}
			out.QueueChanged = true
		case "completed", "failed", "cancelled":
		default:
			return out, employeetask.ErrConflict
		}
		if out.Run.State == employeetask.StateRunning {
			if err = s.recordEmployeeRunInTx(ctx, tx, out.Queue, out.Queue.Status, out.Queue.Result, out.Queue.Error.String); err != nil {
				return out, err
			}
			out.Run, err = directStopRun(ctx, tx, task, req.RunID, req.QueueTaskID)
			if err != nil {
				return out, err
			}
		}
		out.Task, err = store.Get(ctx, task.Scope, task.ID)
		if err != nil {
			return out, err
		}
	}
	projection, pending, err := directStopProjection(ctx, tx, out.Task, out.Run, out.Queue)
	if err != nil {
		return out, err
	}
	out.State, out.ExitConfirmed, out.PendingPredecessor, out.pending = projection.State, projection.ExitConfirmed, projection.PendingPredecessor, pending
	if err = tx.Commit(ctx); err != nil {
		return DirectTaskStopResult{}, err
	}
	return out, nil
}

func directStopRun(ctx context.Context, tx pgx.Tx, task employeetask.Task, runID, queueID string) (employeetask.Run, error) {
	var run employeetask.Run
	err := tx.QueryRow(ctx, `SELECT id::text,task_id::text,queue_task_id::text,goal_revision,input_seq,state,result,result_ref,created_at,finished_at FROM employee_task_run WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND id=$5::uuid AND queue_task_id=$6::uuid`, task.Scope.WorkspaceID, task.Scope.AgentID, task.Scope.TenantOrgID, task.ID, runID, queueID).Scan(&run.ID, &run.TaskID, &run.QueueTaskID, &run.GoalRevision, &run.InputSeq, &run.State, &run.Result, &run.ResultRef, &run.CreatedAt, &run.FinishedAt)
	return run, mapEmployeeTaskSteerError(err)
}

func directStopQueueMatches(task employeetask.Task, queue db.AgentTaskQueue) bool {
	direct, valid := ParseDirectTaskContext(queue)
	return valid && direct.EmployeeTaskID == task.ID && direct.WorkspaceID == task.Scope.WorkspaceID && util.UUIDToString(queue.AgentID) == task.Scope.AgentID
}

func directStopExitConfirmed(queue db.AgentTaskQueue) bool {
	// A completed result does not prove the Task-owned process group exited.
	if taskProcessStopPending(queue) {
		return false
	}
	var evidence struct {
		StoppedAt string `json:"process_stopped_at"`
	}
	if json.Unmarshal(queue.Context, &evidence) == nil && evidence.StoppedAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, evidence.StoppedAt); err == nil {
			return true
		}
	}
	return queue.Status == "cancelled" && !queue.DispatchedAt.Valid && !queue.StartedAt.Valid
}

func directStopProjection(ctx context.Context, tx pgx.Tx, task employeetask.Task, run employeetask.Run, queue db.AgentTaskQueue) (DirectTaskExecutionState, []db.AgentTaskQueue, error) {
	out := DirectTaskExecutionState{TaskID: task.ID, RunID: run.ID, QueueTaskID: run.QueueTaskID, TaskState: task.State, QueueState: queue.Status, State: string(task.State)}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee_task_entry WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND task_id=$4::uuid AND kind='input' AND payload->>'operation'='stop')`, task.Scope.WorkspaceID, task.Scope.AgentID, task.Scope.TenantOrgID, task.ID).Scan(&out.StopRequested); err != nil {
		return out, nil, err
	}
	if run.ID == "" {
		out.ExitConfirmed = true
		if task.State == employeetask.StateCancelled {
			out.State = "stopped"
		}
		return out, nil, nil
	}
	if !directStopQueueMatches(task, queue) {
		return out, nil, ErrDirectTaskAccessDenied
	}
	out.State, out.ExitConfirmed = queue.Status, directStopExitConfirmed(queue)
	if queue.Status == "cancelled" {
		out.State = "unconfirmed"
		if out.ExitConfirmed {
			out.State = "stopped"
		}
	}
	var pending []db.AgentTaskQueue
	if taskProcessStopPending(queue) {
		pending = append(pending, queue)
	}
	// A cancelled, unclaimed successor does not prove its predecessor exited.
	// Walk only existing same-Task links; never follow an arbitrary queue locator.
	seen := map[string]bool{util.UUIDToString(queue.ID): true}
	current := queue
	for {
		var link struct {
			Predecessor string `json:"steer_predecessor_task_id"`
		}
		if json.Unmarshal(current.Context, &link) != nil {
			return out, nil, employeetask.ErrInvalid
		}
		if link.Predecessor == "" {
			break
		}
		id, err := util.ParseUUID(link.Predecessor)
		if err != nil || seen[link.Predecessor] {
			return out, nil, employeetask.ErrInvalid
		}
		seen[link.Predecessor] = true
		previous, err := db.New(tx).GetAgentTask(ctx, id)
		if err != nil {
			return out, nil, err
		}
		priorRun, err := directRunByQueue(ctx, tx, id)
		if err != nil || priorRun.TaskID != task.ID || !directStopQueueMatches(task, previous) {
			return out, nil, ErrDirectTaskAccessDenied
		}
		if _, err = directStopRun(ctx, tx, task, priorRun.ID, link.Predecessor); err != nil {
			return out, nil, err
		}
		// Only an existing cancellation extends this stop's exit obligation.
		// A historical completed/failed Run is not a background-service stop.
		if previous.Status == "cancelled" && !directStopExitConfirmed(previous) {
			out.PendingPredecessor, out.ExitConfirmed = true, false
			out.State = "unconfirmed"
		}
		if taskProcessStopPending(previous) {
			pending = append(pending, previous)
		}
		current = previous
	}
	if len(pending) > 0 {
		out.State, out.ExitConfirmed = "stopping", false
	}
	return out, pending, nil
}

func (s *TaskService) ReadDirectTaskExecutionState(ctx context.Context, tx pgx.Tx, scope employeetask.Scope, taskID string) (DirectTaskExecutionState, error) {
	var out DirectTaskExecutionState
	if tx == nil {
		return out, employeetask.ErrInvalid
	}
	store := employeetask.NewStore(tx)
	task, err := store.Get(ctx, scope, taskID)
	if err != nil {
		return out, err
	}
	if task.OwnerLoop != employeetask.LoopEmployee || task.DispatchMode != employeetask.DispatchDirect || task.Scope.Kind != employeetask.ScopeScene || task.IssueID != "" {
		return out, ErrDirectTaskAccessDenied
	}
	run, err := store.LatestRun(ctx, scope, taskID)
	if errors.Is(err, employeetask.ErrNotFound) {
		run, err = employeetask.Run{}, nil
	}
	if err != nil {
		return out, err
	}
	var queue db.AgentTaskQueue
	if run.QueueTaskID != "" {
		id, err := util.ParseUUID(run.QueueTaskID)
		if err != nil {
			return out, err
		}
		queue, err = db.New(tx).GetAgentTask(ctx, id)
		if err != nil {
			return out, err
		}
	}
	out, _, err = directStopProjection(ctx, tx, task, run, queue)
	return out, err
}

// NotifyDirectTaskStop is called only after the outer Host transaction commits.
// Replays rearm the existing observer without cancelling or selecting new work.
func (s *TaskService) NotifyDirectTaskStop(ctx context.Context, result DirectTaskStopResult) {
	if s == nil || s.Queries == nil {
		return
	}
	if result.QueueChanged {
		s.captureTaskCancelled(ctx, result.Queue)
		s.ReconcileAgentStatus(ctx, result.Queue.AgentID)
		s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, result.Queue)
		s.NotifyTaskFinished(result.Queue)
		if s.CompletionNotifier != nil {
			s.CompletionNotifier.NotifyTaskCompletion()
		}
	}
	for _, queue := range result.pending {
		if result.QueueChanged && queue.ID == result.Queue.ID {
			continue
		}
		s.broadcastTaskEvent(ctx, protocol.EventTaskCancelled, queue)
		s.NotifyTaskFinished(queue)
	}
}

// ReconcileEmployeeTaskStops recovers commit-before-notify only for persisted
// stop intents and their existing pending predecessor chain. It never cancels.
func (s *TaskService) ReconcileEmployeeTaskStops(ctx context.Context, limit int) (int, error) {
	if s == nil || s.TxStarter == nil || limit < 1 || limit > 1000 {
		return 0, employeetask.ErrInvalid
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `WITH RECURSIVE targets AS (
 SELECT DISTINCT t.id,t.workspace_id,t.agent_id,t.tenant_org_id,t.scene_id,r.id AS run_id,r.queue_task_id
 FROM employee_task_entry e JOIN employee_task t ON t.id=e.task_id AND t.workspace_id=e.workspace_id AND t.agent_id=e.agent_id AND t.tenant_org_id=e.tenant_org_id
 JOIN employee_task_run r ON r.task_id=t.id AND r.workspace_id=t.workspace_id AND r.agent_id=t.agent_id AND r.tenant_org_id=t.tenant_org_id AND r.id=e.run_id AND r.id::text=e.payload->>'run_id' AND r.queue_task_id::text=e.payload->>'queue_task_id'
 WHERE e.kind='input' AND e.payload->>'operation'='stop' AND t.owner_loop='employee' AND t.dispatch_mode='direct' AND t.scope_kind='scene'
 ), lineage AS (
 SELECT t.id AS task_id,t.queue_task_id AS target_id,q.id,q.context,q.status,ARRAY[q.id] AS visited FROM targets t JOIN agent_task_queue q ON q.id=t.queue_task_id AND q.agent_id=t.agent_id
 UNION ALL
 SELECT l.task_id,l.target_id,q.id,q.context,q.status,l.visited || q.id FROM lineage l
 JOIN employee_task_run r ON r.task_id=l.task_id AND r.queue_task_id::text=l.context->>'steer_predecessor_task_id'
 JOIN targets t ON t.id=l.task_id AND t.queue_task_id=l.target_id AND t.workspace_id=r.workspace_id AND t.agent_id=r.agent_id AND t.tenant_org_id=r.tenant_org_id
 JOIN agent_task_queue q ON q.id=r.queue_task_id AND q.agent_id=t.agent_id WHERE NOT q.id=ANY(l.visited)
 ) SELECT t.workspace_id::text,t.agent_id::text,t.tenant_org_id,t.scene_id::text,t.id::text,t.run_id::text,t.queue_task_id::text FROM targets t
 WHERE EXISTS(SELECT 1 FROM lineage l WHERE l.task_id=t.id AND l.target_id=t.queue_task_id AND l.status='cancelled' AND l.context->>'process_stop_pending'='true') ORDER BY t.id,t.queue_task_id LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type target struct {
		scope            employeetask.Scope
		task, run, queue string
	}
	var targets []target
	for rows.Next() {
		var item target
		item.scope.Kind = employeetask.ScopeScene
		if err = rows.Scan(&item.scope.WorkspaceID, &item.scope.AgentID, &item.scope.TenantOrgID, &item.scope.Scene.SceneID, &item.task, &item.run, &item.queue); err != nil {
			break
		}
		targets = append(targets, item)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return 0, err
	}
	var results []DirectTaskStopResult
	for _, target := range targets {
		task, err := employeetask.NewStore(tx).Get(ctx, target.scope, target.task)
		if err != nil {
			return 0, err
		}
		run, err := directStopRun(ctx, tx, task, target.run, target.queue)
		if err != nil {
			return 0, err
		}
		id, err := util.ParseUUID(target.queue)
		if err != nil {
			return 0, err
		}
		queue, err := db.New(tx).GetAgentTask(ctx, id)
		if err != nil {
			return 0, err
		}
		projection, pending, err := directStopProjection(ctx, tx, task, run, queue)
		if err != nil {
			return 0, err
		}
		if len(pending) > 0 {
			results = append(results, DirectTaskStopResult{Task: task, Run: run, Queue: queue, State: projection.State, ExitConfirmed: projection.ExitConfirmed, PendingPredecessor: projection.PendingPredecessor, pending: pending})
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	for _, result := range results {
		s.NotifyDirectTaskStop(ctx, result)
	}
	return len(results), nil
}
