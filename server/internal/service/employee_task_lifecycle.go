package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/redact"
)

func (s *TaskService) recordEmployeeRunInTx(ctx context.Context, tx pgx.Tx, task db.AgentTaskQueue, status string, result []byte, errMessage string) error {
	c, ok := ParseDirectTaskContext(task)
	if !ok {
		if IsEmployeeDirectTask(task) {
			return ErrDirectTaskAccessDenied
		}
		return recordEmployeeIssueResult(ctx, tx, task, status, result, errMessage)
	}
	if tx == nil {
		return errors.New("employee run terminal transition requires transaction")
	}
	// An automation-origin execution records its result only against the
	// receipt it was admitted from; a forged locator fails closed.
	if c.AutomationOrigin != nil {
		if _, err := LoadAutomationOrigin(ctx, tx, task); errors.Is(err, ErrAutomationOriginInvalid) {
			return ErrDirectTaskAccessDenied
		} else if err != nil {
			return err
		}
	}
	run, err := directRunByQueue(ctx, tx, task.ID)
	if err != nil {
		return err
	}
	var scope employeetask.Scope
	err = tx.QueryRow(ctx, `SELECT workspace_id::text,agent_id::text,tenant_org_id,scope_kind,COALESCE(scene_id::text,''),COALESCE(legacy_id::text,'') FROM employee_task WHERE id=$1::uuid`, run.TaskID).Scan(&scope.WorkspaceID, &scope.AgentID, &scope.TenantOrgID, &scope.Kind, &scope.Scene.SceneID, &scope.LegacyID)
	if err != nil {
		return err
	}
	if c.EmployeeTaskID != run.TaskID || c.WorkspaceID != scope.WorkspaceID || util.UUIDToString(task.AgentID) != scope.AgentID {
		return ErrDirectTaskAccessDenied
	}
	state := employeetask.StateFailed
	body := redact.Text(errMessage)
	switch status {
	case "completed":
		state = employeetask.StateSucceeded
		body = taskExecutionUpdateResultMessage(result)
	case "cancelled", "canceled":
		state = employeetask.StateCancelled
		body = redact.Text(task.Error.String)
		if body == "" {
			body = "task cancelled"
		}
	case "failed":
	default:
		return nil
	}
	recorded, _, err := employeetask.NewStore(tx).RecordResult(ctx, scope, run.TaskID, employeetask.ResultParams{Source: employeetask.Source{Namespace: "queue_terminal", Key: util.UUIDToString(task.ID)}, RunID: run.ID, State: state, Result: body, ResultRef: "agent_task_queue:" + util.UUIDToString(task.ID)})
	if err != nil {
		return err
	}
	releaseEmployeeDependentsTx(ctx, tx, recorded, employeetask.Source{Namespace: "queue_terminal", Key: util.UUIDToString(task.ID)})
	return nil
}

// releaseEmployeeDependentsTx applies a Task's terminal fact to goals blocked
// by it, inside the transaction that recorded the fact. Only a v1 Task ends
// with its Run; a v2 goal ends by explicit completion or a human stop, whose
// writers release it the same way. A savepoint keeps a dependent's refusal
// from failing the queue transition; the Host's release reconciler retries.
func releaseEmployeeDependentsTx(ctx context.Context, tx pgx.Tx, task employeetask.Task, source employeetask.Source) {
	if task.Lifecycle() != employeetask.LifecycleV1 || (task.State != employeetask.StateSucceeded && task.State != employeetask.StateFailed && task.State != employeetask.StateCancelled) {
		return
	}
	if _, err := ReleaseEmployeeDependentsTx(ctx, tx, task, source); err != nil {
		slog.WarnContext(ctx, "employee dependent release deferred", "task_id", task.ID, "error", err)
	}
}

// ReleaseEmployeeDependentsTx runs employeetask.ReleaseUpstreamWaitTx for a
// terminal Task inside a savepoint of tx. A refusal or failure rolls back only
// the savepoint.
func ReleaseEmployeeDependentsTx(ctx context.Context, tx pgx.Tx, task employeetask.Task, source employeetask.Source) (employeetask.UpstreamRelease, error) {
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return employeetask.UpstreamRelease{}, err
	}
	defer savepoint.Rollback(ctx)
	release, err := employeetask.ReleaseUpstreamWaitTx(ctx, savepoint, task.Scope, task.ID, source)
	if err != nil {
		return release, err
	}
	return release, savepoint.Commit(ctx)
}

// ReconcileEmployeeRuns repairs terminal queue rows from cancel, launch failure
// and sweeper paths after a crash. PostgreSQL is the truth; events only wake work.
// Replaying the deterministic result source never creates a second result entry.
func (s *TaskService) ReconcileEmployeeRuns(ctx context.Context, limit int) (int, error) {
	if s == nil || s.TxStarter == nil {
		return 0, errors.New("employee run reconciliation requires transaction")
	}
	if limit < 1 || limit > 1000 {
		return 0, employeetask.ErrInvalid
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT q.id FROM employee_task_run r JOIN employee_task t ON t.id=r.task_id JOIN agent_task_queue q ON q.id=r.queue_task_id WHERE t.dispatch_mode IN ('direct','issue') AND r.state='running' AND q.status IN ('completed','failed','cancelled') ORDER BY q.created_at LIMIT $1`, limit)
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
	// Release the discovery snapshot before acquiring domain locks, so teardown
	// and completion use the same workspace -> task lock order.
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		err = s.runInTxWithHandle(ctx, func(q *db.Queries, tx pgx.Tx) error {
			task, err := q.GetAgentTask(ctx, id)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			return s.recordEmployeeRunInTx(ctx, tx, task, task.Status, task.Result, task.Error.String)
		})
		if err != nil {
			return count, fmt.Errorf("reconcile employee run %s: %w", util.UUIDToString(id), err)
		}
		count++
	}
	associated, err := s.reconcileEmployeeIssueQueues(ctx, limit)
	if err != nil {
		return count + associated, err
	}
	retries, err := s.reconcileEmployeeIssueRetries(ctx, limit)
	return count + associated + retries, err
}

// Lock the parent before mutating the queue. Workspace teardown locks the same
// parent before deleting either queue or EmployeeTask rows, avoiding lock cycles.
func lockEmployeeRunWorkspace(ctx context.Context, tx pgx.Tx, queueID pgtype.UUID) error {
	if tx == nil {
		return nil
	}
	var id pgtype.UUID
	err := tx.QueryRow(ctx, `SELECT w.id FROM workspace w JOIN employee_task t ON t.workspace_id=w.id JOIN employee_task_run r ON r.task_id=t.id AND r.workspace_id=t.workspace_id AND r.agent_id=t.agent_id WHERE r.queue_task_id=$1 FOR KEY SHARE OF w`, queueID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return err
}
