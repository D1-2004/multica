package service

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/redact"
)

func refreshEmployeeIssueTerminal(ctx context.Context, tx pgx.Tx, task employeetask.Task) (employeetask.Task, error) {
	if task.ActiveRunID == "" {
		return task, nil
	}
	var queueID pgtype.UUID
	err := tx.QueryRow(ctx, `SELECT queue_task_id FROM employee_task_run WHERE id=$1::uuid AND task_id=$2::uuid AND workspace_id=$3::uuid`, task.ActiveRunID, task.ID, task.Scope.WorkspaceID).Scan(&queueID)
	if err != nil {
		return task, err
	}
	queue, err := db.New(tx).GetAgentTask(ctx, queueID)
	if err != nil {
		return task, err
	}
	if queue.Status != "completed" && queue.Status != "failed" && queue.Status != "cancelled" {
		return task, nil
	}
	if err = recordEmployeeIssueResult(ctx, tx, queue, queue.Status, queue.Result, queue.Error.String); err != nil {
		return task, err
	}
	return employeetask.NewStore(tx).Get(ctx, task.Scope, task.ID)
}

// validateEmployeeIssueQueue reads authoritative bindings even for replays.
// Keep this in the existing backend transaction, before recording aggregate
// facts. Do not take an Issue row lock here: retry callers already hold queue
// locks, and adding the inverse Issue-follow-up lock order would deadlock.
func validateEmployeeIssueQueue(ctx context.Context, tx pgx.Tx, task employeetask.Task, queue db.AgentTaskQueue) error {
	if tx == nil || !queue.ID.Valid {
		return employeetask.ErrInvalid
	}
	current, err := employeetask.NewStore(tx).Get(ctx, task.Scope, task.ID)
	if err != nil {
		return err
	}
	if current.DispatchMode != employeetask.DispatchIssue || current.IssueID == "" || current.IssueID != task.IssueID ||
		util.UUIDToString(queue.IssueID) != current.IssueID || util.UUIDToString(queue.AgentID) != current.Scope.AgentID {
		return employeetask.ErrNotFound
	}
	var accepted bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM agent_task_queue q
 JOIN issue i ON i.id=q.issue_id
 JOIN agent a ON a.id=q.agent_id
 WHERE q.id=$1 AND q.agent_id=$2::uuid AND q.issue_id=$3::uuid
   AND i.workspace_id=$4::uuid AND a.workspace_id=$4::uuid)`,
		queue.ID, current.Scope.AgentID, current.IssueID, current.Scope.WorkspaceID).Scan(&accepted)
	if err != nil {
		return err
	}
	if !accepted {
		return employeetask.ErrNotFound
	}
	return nil
}

func mapEmployeeIssueQueue(ctx context.Context, tx pgx.Tx, task employeetask.Task, queue db.AgentTaskQueue, actor, body string, inputSeq int64) error {
	if err := validateEmployeeIssueQueue(ctx, tx, task, queue); err != nil {
		return err
	}
	prior, err := directRunByQueue(ctx, tx, queue.ID)
	if err == nil {
		if prior.TaskID != task.ID {
			return employeetask.ErrConflict
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	task, err = refreshEmployeeIssueTerminal(ctx, tx, task)
	if err != nil {
		return err
	}
	store := employeetask.NewStore(tx)
	if task.OwnerLoop == employeetask.LoopCoordinator && task.DispatchMode == employeetask.DispatchIssue {
		_, err = store.ObserveBackendRun(ctx, task.Scope, task.ID, employeetask.ObserveBackendRunParams{Source: employeetask.Source{Namespace: "issue_queue", Key: util.UUIDToString(queue.ID)}, QueueTaskID: util.UUIDToString(queue.ID), GoalRevision: task.GoalRevision, InputSeq: inputSeq, ExpectedVersion: task.Version})
		if err != nil {
			return err
		}
		return recordEmployeeIssueResult(ctx, tx, queue, queue.Status, queue.Result, queue.Error.String)
	}
	if task.State == employeetask.StateSucceeded {
		task, _, err = store.Resume(ctx, task.Scope, task.ID, employeetask.ResumeParams{Source: employeetask.Source{Namespace: "issue_queue_resume", Key: util.UUIDToString(queue.ID)}, ActorRef: actor, Body: body, ExpectedVersion: task.Version})
		if err != nil {
			return err
		}
	}
	if task.State == employeetask.StateFailed || task.State == employeetask.StateCancelled {
		return employeetask.ErrRunNotReady
	}
	_, err = store.StartRun(ctx, task.Scope, task.ID, employeetask.StartRunParams{Source: employeetask.Source{Namespace: "issue_queue", Key: util.UUIDToString(queue.ID)}, QueueTaskID: util.UUIDToString(queue.ID), InputSeq: inputSeq, ExpectedVersion: task.Version})
	if err != nil {
		return err
	}
	return recordEmployeeIssueResult(ctx, tx, queue, queue.Status, queue.Result, queue.Error.String)
}

// Only a previously mapped queue may complete an Issue-backed task. This does
// not emit Employee notices; Coordinator's existing completion contract owns delivery.
func recordEmployeeIssueResult(ctx context.Context, tx pgx.Tx, queue db.AgentTaskQueue, status string, result []byte, errMessage string) error {
	if tx == nil || !queue.IssueID.Valid {
		return nil
	}
	if status != "completed" && status != "failed" && status != "cancelled" && status != "canceled" {
		return nil
	}
	issue, err := db.New(tx).GetIssue(ctx, queue.IssueID)
	if err != nil {
		return err
	}
	issue.AssigneeID = queue.AgentID
	task, err := employeeTaskByIssue(ctx, tx, issue)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	run, err := directRunByQueue(ctx, tx, queue.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if run.TaskID != task.ID || task.Scope.AgentID != util.UUIDToString(queue.AgentID) {
		return employeetask.ErrNotFound
	}
	if err := validateEmployeeIssueQueue(ctx, tx, task, queue); err != nil {
		return err
	}
	persisted, err := db.New(tx).GetAgentTask(ctx, queue.ID)
	if err != nil {
		return err
	}
	if persisted.AgentID != queue.AgentID || persisted.IssueID != queue.IssueID {
		return employeetask.ErrNotFound
	}
	if status == "canceled" {
		status = "cancelled"
	}
	if persisted.Status != status {
		return employeetask.ErrConflict
	}
	// The callback signals which transition to observe; durable backend fields
	// own the terminal fact, including when the caller holds an older snapshot.
	result, errMessage = persisted.Result, persisted.Error.String
	state := employeetask.StateFailed
	body := redact.Text(errMessage)
	if status == "completed" {
		state = employeetask.StateSucceeded
		body = taskExecutionUpdateResultMessage(result)
	} else if status == "cancelled" || status == "canceled" {
		state = employeetask.StateCancelled
		if body == "" {
			body = "task cancelled"
		}
	}
	_, _, err = employeetask.NewStore(tx).RecordResult(ctx, task.Scope, task.ID, employeetask.ResultParams{Source: employeetask.Source{Namespace: "queue_terminal", Key: util.UUIDToString(queue.ID)}, RunID: run.ID, State: state, Result: body, ResultRef: "agent_task_queue:" + util.UUIDToString(queue.ID)})
	return err
}

// reconcileEmployeeIssueQueues links only durable follow-ups whose exact source
// entries are already accepted. It never guesses the latest queue for an Issue.
func (s *TaskService) reconcileEmployeeIssueQueues(ctx context.Context, limit int) (int, error) {
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT DISTINCT q.id,q.created_at FROM coordinator_issue_follow_up f
 JOIN employee_task t ON t.issue_id=f.issue_id AND t.workspace_id=f.workspace_id AND t.agent_id=f.agent_id AND t.dispatch_mode='issue'
 JOIN employee_task_entry e ON e.task_id=t.id AND e.workspace_id=t.workspace_id AND e.source_namespace=$1 AND e.source_key=f.idempotency_key
 JOIN agent_task_queue q ON q.id=f.task_id AND q.issue_id=f.issue_id AND q.agent_id=f.agent_id
 WHERE NOT EXISTS(SELECT 1 FROM employee_task_run r WHERE r.queue_task_id=q.id)
 AND (t.owner_loop='coordinator' OR t.state NOT IN ('failed','cancelled')) ORDER BY q.created_at,q.id LIMIT $2`, coordinatorIssueInputNamespace, limit)
	if err != nil {
		return 0, err
	}
	ids := []pgtype.UUID{}
	for rows.Next() {
		var id pgtype.UUID
		var created pgtype.Timestamptz
		if err = rows.Scan(&id, &created); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		err = s.runInTxWithHandle(ctx, func(q *db.Queries, tx pgx.Tx) error {
			queue, err := q.GetAgentTask(ctx, id)
			if err != nil {
				return err
			}
			issue, err := q.GetIssue(ctx, queue.IssueID)
			if err != nil {
				return err
			}
			if err = lockEmployeeIssueWorkspace(ctx, tx, issue.WorkspaceID); err != nil {
				return err
			}
			if _, err = q.LockIssueForExternalFollowUp(ctx, db.LockIssueForExternalFollowUpParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID}); err != nil {
				return err
			}
			return mapEmployeeIssueFollowUpQueue(ctx, tx, issue, queue)
		})
		if errors.Is(err, employeetask.ErrRunNotReady) || errors.Is(err, employeetask.ErrActiveRun) {
			continue
		}
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func mapEmployeeIssueFollowUpQueue(ctx context.Context, tx pgx.Tx, issue db.Issue, queue db.AgentTaskQueue) error {
	task, err := employeeTaskByIssue(ctx, tx, issue)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var actor, body string
	var inputSeq int64
	err = tx.QueryRow(ctx, `SELECT e.actor_ref,e.body,e.seq FROM coordinator_issue_follow_up f JOIN employee_task_entry e
 ON e.task_id=$1::uuid AND e.workspace_id=f.workspace_id AND e.agent_id=f.agent_id AND e.source_namespace=$2 AND e.source_key=f.idempotency_key
 WHERE f.task_id=$3 AND f.issue_id=$4 AND f.workspace_id=$5 ORDER BY e.seq DESC LIMIT 1`, task.ID, coordinatorIssueInputNamespace, queue.ID, issue.ID, issue.WorkspaceID).Scan(&actor, &body, &inputSeq)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return mapEmployeeIssueQueue(ctx, tx, task, queue, actor, body, inputSeq)
}

// Employee-owned follow-ups retain their termination fence. Coordinator-owned
// work keeps the existing Issue backend scheduling and only records its facts.
func prepareEmployeeIssueFollowUp(ctx context.Context, tx pgx.Tx, issue db.Issue) error {
	task, err := employeeTaskByIssue(ctx, tx, issue)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	task, err = refreshEmployeeIssueTerminal(ctx, tx, task)
	if err != nil {
		return err
	}
	if task.OwnerLoop != employeetask.LoopCoordinator && (task.State == employeetask.StateFailed || task.State == employeetask.StateCancelled) {
		return employeetask.ErrRunNotReady
	}
	return nil
}

// Existing Issue scheduling owns retries. Observe only the exact retry lineage
// persisted by that backend; this function never creates or wakes a queue task.
func (s *TaskService) reconcileEmployeeIssueRetries(ctx context.Context, limit int) (int, error) {
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT child.id FROM agent_task_queue child
 JOIN agent_task_queue parent ON parent.id=child.retry_of_task_id AND child.parent_task_id=parent.id AND child.agent_id=parent.agent_id AND child.issue_id=parent.issue_id AND child.attempt=parent.attempt+1
 JOIN employee_task_run prior ON prior.queue_task_id=parent.id
 JOIN employee_task t ON t.id=prior.task_id AND t.workspace_id=prior.workspace_id AND t.agent_id=child.agent_id AND t.issue_id=child.issue_id
 WHERE t.owner_loop='coordinator' AND t.dispatch_mode='issue' AND parent.status='failed'
 AND NOT EXISTS(SELECT 1 FROM employee_task_run mapped WHERE mapped.queue_task_id=child.id)
 ORDER BY child.created_at,child.id LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	ids := []pgtype.UUID{}
	for rows.Next() {
		var id pgtype.UUID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		err = s.runInTxWithHandle(ctx, func(q *db.Queries, tx pgx.Tx) error {
			child, err := q.GetAgentTask(ctx, id)
			if err != nil {
				return err
			}
			return observeEmployeeIssueRetryInTx(ctx, tx, child)
		})
		if errors.Is(err, employeetask.ErrActiveRun) {
			continue
		}
		if err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

// Called in the existing retry transaction, after the backend creates its child.
// Queue lineage is immutable evidence; no Issue row lock is acquired after the
// parent queue lock, avoiding the Issue-follow-up/terminal lock-order cycle.
func observeEmployeeIssueRetryInTx(ctx context.Context, tx pgx.Tx, child db.AgentTaskQueue) error {
	if tx == nil || !child.IssueID.Valid || !child.RetryOfTaskID.Valid {
		return nil
	}
	q := db.New(tx)
	persisted, err := q.GetAgentTask(ctx, child.ID)
	if err != nil {
		return err
	}
	if persisted.AgentID != child.AgentID || persisted.IssueID != child.IssueID ||
		persisted.RetryOfTaskID != child.RetryOfTaskID || persisted.ParentTaskID != child.ParentTaskID || persisted.Attempt != child.Attempt {
		return employeetask.ErrConflict
	}
	child = persisted
	prior, err := directRunByQueue(ctx, tx, child.RetryOfTaskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	parent, err := q.GetAgentTask(ctx, child.RetryOfTaskID)
	if err != nil {
		return err
	}
	if parent.Status != "failed" || child.ParentTaskID != parent.ID || child.AgentID != parent.AgentID || child.IssueID != parent.IssueID || child.Attempt != parent.Attempt+1 {
		return employeetask.ErrConflict
	}
	issue, err := q.GetIssue(ctx, child.IssueID)
	if err != nil {
		return err
	}
	if err = lockEmployeeIssueWorkspace(ctx, tx, issue.WorkspaceID); err != nil {
		return err
	}
	// Completion can commit while the observer waits for this aggregate. Lock
	// the exact parent Run's Task first, then read its current state/version;
	// a previously read version must not turn a normal terminal race into a
	// source conflict that rolls back an already-accepted legacy retry.
	var lockedTaskID string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM employee_task WHERE id=$1::uuid AND workspace_id=$2 AND agent_id=$3 AND issue_id=$4 AND dispatch_mode='issue' FOR UPDATE`, prior.TaskID, issue.WorkspaceID, child.AgentID, child.IssueID).Scan(&lockedTaskID); err != nil {
		return err
	}
	issue.AssigneeID = child.AgentID
	task, err := employeeTaskByIssue(ctx, tx, issue)
	if err != nil {
		return err
	}
	if task.OwnerLoop != employeetask.LoopCoordinator || task.DispatchMode != employeetask.DispatchIssue {
		return employeetask.ErrInvalid
	}
	if prior.TaskID != task.ID {
		return employeetask.ErrConflict
	}
	if err := validateEmployeeIssueQueue(ctx, tx, task, child); err != nil {
		return err
	}
	if existing, err := directRunByQueue(ctx, tx, child.ID); err == nil {
		if existing.TaskID != task.ID {
			return employeetask.ErrConflict
		}
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	task, err = refreshEmployeeIssueTerminal(ctx, tx, task)
	if err != nil {
		return err
	}
	_, err = employeetask.NewStore(tx).ObserveBackendRun(ctx, task.Scope, task.ID, employeetask.ObserveBackendRunParams{Source: employeetask.Source{Namespace: "issue_queue", Key: util.UUIDToString(child.ID)}, QueueTaskID: util.UUIDToString(child.ID), GoalRevision: prior.GoalRevision, InputSeq: prior.InputSeq, ExpectedVersion: task.Version})
	if err != nil {
		return err
	}
	return recordEmployeeIssueResult(ctx, tx, child, child.Status, child.Result, child.Error.String)
}

// The legacy scheduler has already accepted this retry. A newer active domain
// Run can delay its mapping, but must not veto that existing scheduling rule.
// The queue and exact parent lineage remain durable; reconciliation retries the
// observation after the newer Run finishes, including already-terminal children.
func observeOrDeferEmployeeIssueRetryInTx(ctx context.Context, tx pgx.Tx, child db.AgentTaskQueue) error {
	err := observeEmployeeIssueRetryInTx(ctx, tx, child)
	if errors.Is(err, employeetask.ErrActiveRun) {
		return nil
	}
	return err
}
