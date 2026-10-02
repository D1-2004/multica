package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func issueBackendFixture(t *testing.T) (issueFollowUpFixture, *EmployeeIssueBackend, EmployeeIssueCreateParams) {
	t.Helper()
	f := newIssueFollowUpFixture(t)
	task := f.svc.TaskService
	issues := NewIssueService(task.Queries, task.TxStarter, task.Bus, analytics.NoopClient{}, task)
	backend := NewEmployeeIssueBackend(issues, f.svc)
	scope := employeetask.Scope{WorkspaceID: util.UUIDToString(f.params.Issue.WorkspaceID), AgentID: util.UUIDToString(f.params.Issue.AssigneeID), Kind: employeetask.ScopeLegacyIssue, LegacyID: util.UUIDToString(f.params.Issue.ID)}
	p := EmployeeIssueCreateParams{
		Intent: employeetask.CreateParams{Scope: scope, OwnerLoop: employeetask.LoopCoordinator, DispatchMode: employeetask.DispatchIssue, RequesterRef: "dingtalk:requester-1", Definition: employeetask.Definition{Goal: "Prepare a report"}, Input: "Prepare a report", Source: employeetask.Source{Namespace: "coordinator", Key: "window-1/action-1"}},
		Issue:  IssueCreateParams{WorkspaceID: f.params.Issue.WorkspaceID, Title: "Prepare a report", Description: pgtype.Text{String: "Prepare a report", Valid: true}, Status: "todo", Priority: "none", AssigneeType: pgtype.Text{String: "agent", Valid: true}, AssigneeID: f.params.Issue.AssigneeID, CreatorType: "member", CreatorID: f.params.AuthorID, AllowDuplicate: true},
	}
	t.Cleanup(func() {
		ctx := context.Background()
		for _, table := range []string{"employee_task_entry", "employee_task_run", "employee_task", "coordinator_issue_follow_up", "task_message", "agent_task_queue", "comment", "issue"} {
			var err error
			if table == "task_message" {
				_, err = f.pool.Exec(ctx, `DELETE FROM task_message WHERE task_id IN (SELECT id FROM agent_task_queue WHERE agent_id=$1)`, f.params.Issue.AssigneeID)
			} else if table == "agent_task_queue" {
				_, err = f.pool.Exec(ctx, `DELETE FROM agent_task_queue WHERE agent_id=$1`, f.params.Issue.AssigneeID)
			} else {
				_, err = f.pool.Exec(ctx, `DELETE FROM `+table+` WHERE workspace_id=$1`, f.params.Issue.WorkspaceID)
			}
			if err != nil {
				t.Error(err)
			}
		}
	})
	return f, backend, p
}

func TestEmployeeIssueCreateConcurrentReplayAndAtomicLedger(t *testing.T) {
	f, backend, p := issueBackendFixture(t)
	ctx := context.Background()
	errs := make(chan error, 4)
	outcomes := make(chan IssueCreateResult, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { result, err := backend.Create(ctx, p); errs <- err; outcomes <- result })
	}
	wg.Wait()
	close(errs)
	close(outcomes)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first IssueCreateResult
	for result := range outcomes {
		if !first.Issue.ID.Valid {
			first = result
		}
		if first.Issue.ID != result.Issue.ID || result.EnqueuedTask == nil || first.EnqueuedTask.ID != result.EnqueuedTask.ID {
			t.Fatal("duplicate source created distinct Issue/queue")
		}
	}
	var tasks, runs, entries int
	err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_task WHERE issue_id=$1),(SELECT count(*) FROM employee_task_run WHERE queue_task_id=$2),(SELECT count(*) FROM employee_task_entry e JOIN employee_task t ON t.id=e.task_id WHERE t.issue_id=$1)`, first.Issue.ID, first.EnqueuedTask.ID).Scan(&tasks, &runs, &entries)
	if err != nil || tasks != 1 || runs != 1 || entries != 3 {
		t.Fatalf("domain ledger=%d/%d/%d: %v", tasks, runs, entries, err)
	}
	p.Intent.Definition.Goal = "Different work"
	if _, err = backend.Create(ctx, p); !errors.Is(err, employeetask.ErrConflict) {
		t.Fatalf("conflicting replay: %v", err)
	}
}

type employeeIssueSteerWakeup func(string, string)

func (f employeeIssueSteerWakeup) NotifyTaskAvailable(runtimeID, taskID string) {
	f(runtimeID, taskID)
}

func TestEmployeeIssueSteerNotifiesCommittedPredecessorBeforeSuccessor(t *testing.T) {
	f, backend, p := issueBackendFixture(t)
	ctx := context.Background()
	first, err := backend.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now(),dispatched_at=now() WHERE id=$1`, first.EnqueuedTask.ID); err != nil {
		t.Fatal(err)
	}
	launcher := &terminalRecordingLauncher{}
	f.svc.TaskService.RuntimeLauncher = launcher
	wakeups := 0
	f.svc.TaskService.Wakeup = employeeIssueSteerWakeup(func(_, taskID string) {
		if taskID == "" {
			return
		}
		wakeups++
		if len(launcher.terminal) != 1 || launcher.terminal[0].ID != first.EnqueuedTask.ID || !taskProcessStopPending(launcher.terminal[0]) {
			t.Error("successor woke before the runtime observed its stopped predecessor")
		}
		old, queryErr := f.svc.Queries.GetAgentTask(ctx, first.EnqueuedTask.ID)
		if queryErr != nil || old.Status != "cancelled" || !taskProcessStopPending(old) {
			t.Errorf("stop notification preceded the committed cancellation: status=%s err=%v", old.Status, queryErr)
		}
	})
	result, err := backend.Continue(ctx, EmployeeIssueContinueParams{
		Comment:  IssueCommentCreateParams{Issue: first.Issue, AuthorID: f.params.AuthorID, Content: "Correct the report scope", IdempotencyKey: "steer-after-commit", QueueMode: "steer"},
		ActorRef: "member:" + util.UUIDToString(f.params.AuthorID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Task.Status != "queued" || wakeups != 1 || len(launcher.terminal) != 1 {
		t.Fatalf("steer did not notify both committed runs: status=%s wakeups=%d terminal=%d", result.Task.Status, wakeups, len(launcher.terminal))
	}
}

func TestEmployeeIssueContinueLegacyAndCompletedReplay(t *testing.T) {
	f, backend, _ := issueBackendFixture(t)
	ctx := context.Background()
	p := EmployeeIssueContinueParams{Comment: f.params, ActorRef: "dingtalk:actual-author"}
	first, err := backend.Continue(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	var taskID, requester string
	if err = f.pool.QueryRow(ctx, `SELECT id::text,requester_ref FROM employee_task WHERE issue_id=$1`, f.params.Issue.ID).Scan(&taskID, &requester); err != nil {
		t.Fatal(err)
	}
	if requester == p.ActorRef || requester == util.UUIDToString(f.params.AuthorID) {
		t.Fatal("operator/current actor falsely adopted as original requester")
	}
	if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='completed',result='{"message":"done"}' WHERE id=$1`, first.Task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.TaskService.ReconcileEmployeeRuns(ctx, 100); err != nil {
		t.Fatal(err)
	}
	replay, err := backend.Continue(ctx, p)
	if err != nil || replay.Task.ID != first.Task.ID || replay.Comment.ID != first.Comment.ID {
		t.Fatalf("completed replay: %+v %v", replay, err)
	}
	p.Comment.IdempotencyKey = "window-2/action-1"
	p.Comment.Content = "Add delivery dates"
	second, err := backend.Continue(ctx, p)
	if err != nil || second.Task.ID == first.Task.ID {
		t.Fatalf("explicit continuation: %+v %v", second, err)
	}
	var runs int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM employee_task_run WHERE task_id=$1::uuid`, taskID).Scan(&runs); err != nil || runs != 2 {
		t.Fatalf("runs=%d %v", runs, err)
	}
}

func TestEmployeeIssueBusyFollowUpMapsOnlyActualMergedQueue(t *testing.T) {
	f, backend, p := issueBackendFixture(t)
	ctx := context.Background()
	created, err := backend.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	next := EmployeeIssueContinueParams{Comment: IssueCommentCreateParams{Issue: created.Issue, AuthorID: f.params.AuthorID, Content: "Add risks", IdempotencyKey: "follow-1", DispatchContext: []byte(`{"proactive_conversation":true,"external_identity":{"dws":{"uid":"u1","orgId":"o1"}},"dispatch_event_data":{"conversation":{"openConversationId":"cid-test"},"messages":[{"openMsgId":"m2","text":"Add risks"}]}}`)}, ActorRef: "dingtalk:person-2", Proactive: true}
	for _, key := range []string{"follow-1", "follow-2"} {
		next.Comment.IdempotencyKey = key
		out, err := backend.Continue(ctx, next)
		if err != nil || out.Task.ID.Valid {
			t.Fatalf("busy follow-up fabricated a Run: %+v %v", out, err)
		}
	}
	var count int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM employee_task_run WHERE queue_task_id=$1`, created.EnqueuedTask.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(err, count)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='completed' WHERE id=$1`, created.EnqueuedTask.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.TaskService.ReconcileEmployeeRuns(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.svc.ProcessCoordinatorFollowUp(ctx); err != nil || !ok {
		t.Fatalf("actual enqueue: %v %v", ok, err)
	}
	if _, err = f.svc.TaskService.ReconcileEmployeeRuns(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM employee_task_run r JOIN employee_task t ON t.id=r.task_id WHERE t.issue_id=$1`, created.Issue.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("merged queue mappings=%d %v", count, err)
	}
}

func TestEmployeeIssueBusyFailurePreservesAcceptedLegacyFollowUp(t *testing.T) {
	f, backend, p := issueBackendFixture(t)
	ctx := context.Background()
	created, err := backend.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	next := EmployeeIssueContinueParams{Comment: IssueCommentCreateParams{Issue: created.Issue, AuthorID: f.params.AuthorID, Content: "Continue after the current work", IdempotencyKey: "pending-after-failure", DispatchContext: []byte(`{"external_identity":{"dws":{"uid":"u1","orgId":"o1"}},"dispatch_event_data":{"conversation":{"openConversationId":"cid-test"}}}`)}, ActorRef: "dingtalk:person-2", Proactive: true}
	if _, err = backend.Continue(ctx, next); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='failed',error='runner unreachable' WHERE id=$1`, created.EnqueuedTask.ID); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.svc.ProcessCoordinatorFollowUp(ctx); err != nil || !worked {
		t.Fatalf("legacy accepted continuation was blocked: %v %v", worked, err)
	}
	var pending, queues int
	if err = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM coordinator_issue_follow_up WHERE issue_id=$1 AND task_id IS NULL),(SELECT count(*) FROM agent_task_queue WHERE issue_id=$1)`, created.Issue.ID).Scan(&pending, &queues); err != nil || pending != 0 || queues != 2 {
		t.Fatalf("accepted follow-up lost or re-dispatched: %d/%d %v", pending, queues, err)
	}
}

func TestEmployeeIssueReconcileRecoversCommittedFollowUpWithoutRun(t *testing.T) {
	f, backend, p := issueBackendFixture(t)
	ctx := context.Background()
	created, err := backend.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='completed' WHERE id=$1`, created.EnqueuedTask.ID); err != nil {
		t.Fatal(err)
	}
	next := EmployeeIssueContinueParams{Comment: IssueCommentCreateParams{Issue: created.Issue, AuthorID: f.params.AuthorID, Content: "Continue from the accepted result", IdempotencyKey: "recover-pending", DispatchContext: []byte(`{"external_identity":{"dws":{"uid":"u1","orgId":"o1"}},"dispatch_event_data":{"conversation":{"openConversationId":"cid-test"}}}`)}, ActorRef: "dingtalk:person-2", Proactive: true}
	accepted, err := backend.Continue(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	// This is the older worker's real queue/follow-up transaction, before the new mapping hook.
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	q := f.svc.Queries.WithTx(tx)
	tasks := &TaskService{Queries: q, TxStarter: tx, Bus: events.New()}
	queued, err := tasks.EnqueueTaskForIssueWithDispatchContext(ctx, created.Issue, "", next.Comment.DispatchContext, accepted.Comment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE coordinator_issue_follow_up SET task_id=$2 WHERE comment_id=$1`, accepted.Comment.ID, queued.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = f.svc.TaskService.ReconcileEmployeeRuns(ctx, 100); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM employee_task_run WHERE queue_task_id=$1`, queued.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("exact queue recovery: %d %v", count, err)
	}
}

func TestEmployeeIssueRecordsActualLegacyAutoRetry(t *testing.T) {
	f, backend, p := issueBackendFixture(t)
	ctx := context.Background()
	created, err := backend.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := f.svc.Queries.GetAgentTask(ctx, created.EnqueuedTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.MaxAttempts <= 1 {
		t.Fatal("Issue adapter removed the legacy retry budget")
	}
	if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='failed',error='provider unreachable',failure_reason='provider_network' WHERE id=$1`, parent.ID); err != nil {
		t.Fatal(err)
	}
	child, err := f.svc.Queries.CreateRetryTask(ctx, db.CreateRetryTaskParams{ID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = f.svc.TaskService.ReconcileEmployeeRuns(ctx, 100); err != nil {
			t.Fatal(err)
		}
	}
	var oldState, newState string
	var queues int
	if err = f.pool.QueryRow(ctx, `SELECT (SELECT state FROM employee_task_run WHERE queue_task_id=$1),(SELECT state FROM employee_task_run WHERE queue_task_id=$2),(SELECT count(*) FROM agent_task_queue WHERE issue_id=$3)`, parent.ID, child.ID, parent.IssueID).Scan(&oldState, &newState, &queues); err != nil || oldState != "failed" || newState != "running" || queues != 2 {
		t.Fatalf("retry ledger=%s/%s queues=%d err=%v", oldState, newState, queues, err)
	}
}

func TestEmployeeIssueAutoRetryCommitsLedgerBeforeQueuedEvent(t *testing.T) {
	f, backend, p := issueBackendFixture(t)
	ctx := context.Background()
	created, err := backend.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.Bus.Subscribe(protocol.EventTaskQueued, func(event events.Event) {
		var count int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM employee_task_run r JOIN employee_task t ON t.id=r.task_id WHERE t.issue_id=$1`, created.Issue.ID).Scan(&count); err != nil || count != 2 {
			t.Errorf("retry event preceded both Run records: %d %v", count, err)
		}
	})
	if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1`, created.EnqueuedTask.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.TaskService.FailTask(ctx, created.EnqueuedTask.ID, "temporary provider failure", "", "", "agent_error.provider_network", false, ""); err != nil {
		t.Fatal(err)
	}
	var oldState, newState string
	if err = f.pool.QueryRow(ctx, `SELECT old.state,new.state FROM employee_task_run old JOIN agent_task_queue child ON child.retry_of_task_id=old.queue_task_id JOIN employee_task_run new ON new.queue_task_id=child.id WHERE old.queue_task_id=$1`, created.EnqueuedTask.ID).Scan(&oldState, &newState); err != nil || oldState != "failed" || newState != "running" {
		t.Fatalf("atomic retry facts %s/%s: %v", oldState, newState, err)
	}
}

func TestEmployeeIssueRetryWaitsForNewerRunAndEventuallyMaps(t *testing.T) {
	for _, childStatus := range []string{"queued", "completed", "failed", "cancelled"} {
		t.Run(childStatus, func(t *testing.T) {
			f, backend, p := issueBackendFixture(t)
			ctx := context.Background()
			created, err := backend.Create(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='failed',failure_reason='runtime_offline',error='runtime offline' WHERE id=$1`, created.EnqueuedTask.ID); err != nil {
				t.Fatal(err)
			}
			params := f.params
			params.Issue = created.Issue
			params.IdempotencyKey = "newer-continuation"
			params.Content = "Add delivery dates"
			next, err := backend.Continue(ctx, EmployeeIssueContinueParams{Comment: params, ActorRef: "dingtalk:later-requester"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1`, next.Task.ID); err != nil {
				t.Fatal(err)
			}
			parent, err := f.svc.Queries.GetAgentTask(ctx, created.EnqueuedTask.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !retryEligible(parent.FailureReason.String, parent) {
				t.Fatal("legacy parent is not eligible for retry")
			}
			retry, err := f.svc.TaskService.MaybeRetryFailedTask(ctx, parent)
			if err != nil || retry == nil {
				t.Fatalf("observation vetoed accepted legacy retry: child=%v err=%v", retry, err)
			}
			if childStatus != "queued" {
				if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status=$2,completed_at=now(),result='{"output":"Retry fact"}',error='retry terminal detail' WHERE id=$1`, retry.ID, childStatus); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = f.svc.TaskService.ReconcileEmployeeRuns(ctx, 100); err != nil {
				t.Fatalf("pending association prevented reconciliation: %v", err)
			}
			var mapped, active int
			if err = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM employee_task_run WHERE queue_task_id=$1),(SELECT count(*) FROM employee_task_run WHERE agent_id=$2 AND state='running')`, retry.ID, parent.AgentID).Scan(&mapped, &active); err != nil || mapped != 0 || active != 1 {
				t.Fatalf("association replaced newer active Run: mapped=%d active=%d err=%v", mapped, active, err)
			}
			if _, err = f.svc.TaskService.CompleteTask(ctx, next.Task.ID, []byte(`{"output":"Newer continuation done"}`), "", "", false, ""); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err = f.svc.TaskService.ReconcileEmployeeRuns(ctx, 100); err != nil {
					t.Fatal(err)
				}
			}
			want := childStatus
			if want == "queued" {
				want = "running"
			}
			if want == "completed" {
				want = "succeeded"
			}
			var state string
			var count, queues int
			if err = f.pool.QueryRow(ctx, `SELECT (SELECT state FROM employee_task_run WHERE queue_task_id=$1),(SELECT count(*) FROM employee_task_run WHERE queue_task_id=$1),(SELECT count(*) FROM agent_task_queue WHERE issue_id=$2)`, retry.ID, parent.IssueID).Scan(&state, &count, &queues); err != nil || state != want || count != 1 || queues != 3 {
				t.Fatalf("deferred retry was lost or duplicated: state=%s count=%d queues=%d err=%v", state, count, queues, err)
			}
		})
	}
}

func TestEmployeeIssueConcurrentTerminalCannotRollbackEligibleRetry(t *testing.T) {
	f, backend, p := issueBackendFixture(t)
	p.Intent.Scope = employeetask.Scope{WorkspaceID: p.Intent.Scope.WorkspaceID, AgentID: p.Intent.Scope.AgentID, Kind: employeetask.ScopeLegacyIssue}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	created, err := backend.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='failed',failure_reason='runtime_offline',error='runtime lost',completed_at=now() WHERE id=$1`, created.EnqueuedTask.ID); err != nil {
		t.Fatal(err)
	}
	params := f.params
	params.Issue = created.Issue
	params.IdempotencyKey = "later-terminal"
	params.Content = "Add delivery dates"
	next, err := backend.Continue(ctx, EmployeeIssueContinueParams{Comment: params, ActorRef: "dingtalk:human"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1`, next.Task.ID); err != nil {
		t.Fatal(err)
	}
	parent, err := f.svc.Queries.GetAgentTask(ctx, created.EnqueuedTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	held, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback(context.Background())
	var blocker int
	if err := held.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	if _, err := held.Exec(ctx, `UPDATE agent_task_queue SET status='completed',result='{"output":"done"}' WHERE id=$1`, next.Task.ID); err != nil {
		t.Fatal(err)
	}
	task, err := employeeTaskByIssue(ctx, held, created.Issue)
	if err != nil {
		t.Fatal(err)
	}
	run, err := directRunByQueue(ctx, held, next.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = employeetask.NewStore(held).RecordResult(ctx, task.Scope, task.ID, employeetask.ResultParams{Source: employeetask.Source{Namespace: "queue_terminal", Key: util.UUIDToString(next.Task.ID)}, RunID: run.ID, State: employeetask.StateSucceeded, Result: "done", ResultRef: "agent_task_queue:" + util.UUIDToString(next.Task.ID)})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	finished := make(chan struct{})
	t.Cleanup(func() { cancel(); <-finished })
	go func() {
		defer close(finished)
		child, err := f.svc.TaskService.MaybeRetryFailedTask(ctx, parent)
		if err == nil && child == nil {
			err = employeetask.ErrNotFound
		}
		done <- err
	}()
	for {
		var waiting bool
		if err := f.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)) AND wait_event_type='Lock' AND query LIKE '%employee_task%')`, blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("retry did not reach concurrent terminal lock: %v", err)
		case <-ctx.Done():
			t.Fatal("retry never waited for terminal commit")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := held.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("concurrent terminal caused observation to roll back eligible legacy retry: %v", err)
	}
}
