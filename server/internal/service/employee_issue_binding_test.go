package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/employeetask"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestEmployeeIssueMappingRevalidatesPersistedBindings(t *testing.T) {
	for _, change := range []string{"missing_queue", "queue_agent", "queue_issue", "issue_workspace", "agent_workspace"} {
		t.Run(change, func(t *testing.T) {
			f, backend, params := issueBackendFixture(t)
			other := newIssueFollowUpFixture(t)
			ctx := context.Background()
			created, err := backend.Create(ctx, params)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			task, err := employeeTaskByIssue(ctx, tx, created.Issue)
			if err != nil {
				t.Fatal(err)
			}
			queue := *created.EnqueuedTask
			switch change {
			case "missing_queue":
				_, err = tx.Exec(ctx, `DELETE FROM agent_task_queue WHERE id=$1`, queue.ID)
			case "queue_agent":
				_, err = tx.Exec(ctx, `UPDATE agent_task_queue SET agent_id=$2 WHERE id=$1`, queue.ID, other.params.Issue.AssigneeID)
			case "queue_issue":
				_, err = tx.Exec(ctx, `UPDATE agent_task_queue SET issue_id=$2 WHERE id=$1`, queue.ID, other.params.Issue.ID)
			case "issue_workspace":
				_, err = tx.Exec(ctx, `UPDATE issue SET workspace_id=$2 WHERE id=$1`, queue.IssueID, other.params.Issue.WorkspaceID)
			case "agent_workspace":
				_, err = tx.Exec(ctx, `UPDATE agent SET workspace_id=$2,name=name||'-moved-'||id::text WHERE id=$1`, queue.AgentID, other.params.Issue.WorkspaceID)
			}
			if err != nil {
				t.Fatal(err)
			}
			// The caller's old queue object still matches. It is not authority,
			// including when an existing Run makes this look like a replay.
			if err := mapEmployeeIssueQueue(ctx, tx, task, queue, "member:owner", "continue", task.LastEntrySeq); !errors.Is(err, employeetask.ErrNotFound) {
				t.Fatalf("accepted stale %s binding: %v", change, err)
			}
			unchanged, err := employeetask.NewStore(tx).Get(ctx, task.Scope, task.ID)
			if err != nil || unchanged.Version != task.Version || unchanged.ActiveRunID != task.ActiveRunID {
				t.Fatalf("rejected binding mutated Task: %+v %v", unchanged, err)
			}
		})
	}
}

func TestEmployeeIssueTerminalRevalidatesQueueAndReplaysOnce(t *testing.T) {
	f, backend, params := issueBackendFixture(t)
	other := newIssueFollowUpFixture(t)
	ctx := context.Background()
	created, err := backend.Create(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	task, err := employeeTaskByIssue(ctx, tx, created.Issue)
	if err != nil {
		t.Fatal(err)
	}
	queue := *created.EnqueuedTask
	if _, err = tx.Exec(ctx, `UPDATE agent_task_queue SET agent_id=$2 WHERE id=$1`, queue.ID, other.params.Issue.AssigneeID); err != nil {
		t.Fatal(err)
	}
	if err := recordEmployeeIssueResult(ctx, tx, queue, "completed", []byte(`{"output":"FORGED_RESULT"}`), ""); !errors.Is(err, employeetask.ErrNotFound) {
		t.Fatalf("terminal accepted caller-supplied binding: %v", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_task_queue SET agent_id=$2,status='completed',result='{"output":"ACTUAL_RESULT"}' WHERE id=$1`, queue.ID, queue.AgentID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := recordEmployeeIssueResult(ctx, tx, queue, "completed", []byte(`{"output":"ACTUAL_RESULT"}`), ""); err != nil {
			t.Fatal(err)
		}
	}
	var entries int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM employee_task_entry WHERE task_id=$1::uuid AND kind='result'`, task.ID).Scan(&entries); err != nil || entries != 1 {
		t.Fatalf("terminal entries=%d %v", entries, err)
	}
	finished, err := employeetask.NewStore(tx).Get(ctx, task.Scope, task.ID)
	if err != nil || finished.State != employeetask.StateSucceeded || finished.Version != task.Version+1 {
		t.Fatalf("terminal replay: %+v %v", finished, err)
	}
}

func TestEmployeeIssueObservationAndAcceptedQueueShareTransaction(t *testing.T) {
	f, backend, params := issueBackendFixture(t)
	ctx := context.Background()
	created, err := backend.Create(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	parent := *created.EnqueuedTask
	if _, err = tx.Exec(ctx, `UPDATE agent_task_queue SET status='completed' WHERE id=$1`, parent.ID); err != nil {
		t.Fatal(err)
	}
	if err = recordEmployeeIssueResult(ctx, tx, parent, "completed", []byte(`{"output":"DONE"}`), ""); err != nil {
		t.Fatal(err)
	}
	task, err := employeeTaskByIssue(ctx, tx, created.Issue)
	if err != nil {
		t.Fatal(err)
	}
	queue := parent
	queue.ID = pgtype.UUID{Bytes: uuid.New(), Valid: true}
	queue.Status = "queued"
	if err := mapEmployeeIssueQueue(ctx, tx, task, queue, params.Intent.RequesterRef, "continue", task.LastEntrySeq); !errors.Is(err, employeetask.ErrNotFound) {
		t.Fatalf("unpersisted queue accepted as execution fact: %v", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agent_task_queue(id,agent_id,issue_id,runtime_id,status) VALUES($1,$2,$3,$4,'queued')`, queue.ID, queue.AgentID, queue.IssueID, queue.RuntimeID); err != nil {
		t.Fatal(err)
	}
	if err = mapEmployeeIssueQueue(ctx, tx, task, queue, params.Intent.RequesterRef, "continue", task.LastEntrySeq); err != nil {
		t.Fatal(err)
	}
	var visible bool
	query := `SELECT EXISTS(SELECT 1 FROM agent_task_queue WHERE id=$1) OR EXISTS(SELECT 1 FROM employee_task_run WHERE queue_task_id=$1)`
	if err = f.pool.QueryRow(ctx, query, queue.ID).Scan(&visible); err != nil || visible {
		t.Fatalf("observation escaped outer transaction: visible=%v err=%v", visible, err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.pool.QueryRow(ctx, query, queue.ID).Scan(&visible); err != nil || visible {
		t.Fatalf("rollback left queue or execution fact: visible=%v err=%v", visible, err)
	}
}

func TestEmployeeIssueTerminalRequiresPersistedTerminalFacts(t *testing.T) {
	f, backend, params := issueBackendFixture(t)
	ctx := context.Background()
	created, err := backend.Create(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	queue := *created.EnqueuedTask
	if err := recordEmployeeIssueResult(ctx, tx, queue, "completed", []byte(`{"output":"UNCOMMITTED_RESULT"}`), ""); !errors.Is(err, employeetask.ErrConflict) {
		t.Fatalf("nonterminal queue accepted caller completion: %v", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_task_queue SET status='completed',result='{"output":"PERSISTED_RESULT"}' WHERE id=$1`, queue.ID); err != nil {
		t.Fatal(err)
	}
	if err = recordEmployeeIssueResult(ctx, tx, queue, "completed", []byte(`{"output":"STALE_CALLER_RESULT"}`), ""); err != nil {
		t.Fatal(err)
	}
	var result string
	if err = tx.QueryRow(ctx, `SELECT result FROM employee_task_run WHERE queue_task_id=$1`, queue.ID).Scan(&result); err != nil || result != "PERSISTED_RESULT" {
		t.Fatalf("result was not read from the backend: %q %v", result, err)
	}
}

func TestEmployeeIssueRetryRequiresPersistedLineage(t *testing.T) {
	for _, field := range []string{"retry_of_task_id", "parent_task_id", "attempt"} {
		t.Run(field, func(t *testing.T) {
			f, backend, params := issueBackendFixture(t)
			ctx := context.Background()
			created, err := backend.Create(ctx, params)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err = tx.Exec(ctx, `UPDATE agent_task_queue SET status='failed',failure_reason='runtime_offline' WHERE id=$1`, created.EnqueuedTask.ID); err != nil {
				t.Fatal(err)
			}
			child, err := db.New(tx).CreateRetryTask(ctx, db.CreateRetryTaskParams{ID: created.EnqueuedTask.ID})
			if err != nil {
				t.Fatal(err)
			}
			statement := `UPDATE agent_task_queue SET ` + field + `=NULL WHERE id=$1`
			if field == "attempt" {
				statement = `UPDATE agent_task_queue SET attempt=attempt+1 WHERE id=$1`
			}
			if _, err = tx.Exec(ctx, statement, child.ID); err != nil {
				t.Fatal(err)
			}
			if err = observeEmployeeIssueRetryInTx(ctx, tx, child); !errors.Is(err, employeetask.ErrConflict) {
				t.Fatalf("accepted stale %s retry lineage: %v", field, err)
			}
			var mapped int
			if err = tx.QueryRow(ctx, `SELECT count(*) FROM employee_task_run WHERE queue_task_id=$1`, child.ID).Scan(&mapped); err != nil || mapped != 0 {
				t.Fatalf("invalid retry persisted: %d %v", mapped, err)
			}
		})
	}
}
