package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/util"
)

func completedDirectContinuation(t *testing.T) (directFixture, DirectTaskRequest, employeetask.ResumeParams) {
	t.Helper()
	f := directDatabase(t)
	ctx := context.Background()
	first, err := f.service.EnqueueDirectTask(ctx, f.request)
	if err != nil {
		t.Fatal(err)
	}
	completeDirectContinuation(t, f, first)
	request := f.request
	request.Task, err = employeetask.NewStore(f.pool).Get(ctx, request.Task.Scope, request.Task.ID)
	if err != nil || request.Task.State != employeetask.StateSucceeded {
		t.Fatal("first execution did not succeed", request.Task.State, err)
	}
	request.Source = employeetask.Source{Namespace: "employee_scene", Key: "receipt/call/run"}
	request.Prompt = "Continue the completed goal using the new source input"
	request.Context = json.RawMessage(`{"source":{"receipt_id":"receipt","call_id":"call","text":"continue"}}`)
	resume := employeetask.ResumeParams{
		Source:   employeetask.Source{Namespace: "employee_scene", Key: "receipt/call/resume"},
		ActorRef: request.Task.RequesterRef, Body: string(request.Context), ExpectedVersion: request.Task.Version,
	}
	return f, request, resume
}

func completeDirectContinuation(t *testing.T, f directFixture, result DirectTaskResult) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1`, result.Task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CompleteTask(ctx, result.Task.ID, []byte(`{"output":"done"}`), "", "", false, ""); err != nil {
		t.Fatal(err)
	}
}

func continueDirectCommitted(ctx context.Context, f directFixture, prepared PreparedDirectTask, resume employeetask.ResumeParams) (DirectTaskResult, error) {
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		return DirectTaskResult{}, err
	}
	defer tx.Rollback(ctx)
	result, err := f.service.ContinueDirectTaskTx(ctx, tx, prepared, resume)
	if err != nil {
		return DirectTaskResult{}, err
	}
	return result, tx.Commit(ctx)
}

func assertContinuationCounts(t *testing.T, f directFixture, wantQueues, wantRuns, wantResumes int) {
	t.Helper()
	var queues, runs, resumes int
	err := f.pool.QueryRow(context.Background(), `SELECT
	 (SELECT count(*) FROM agent_task_queue WHERE agent_id=$1::uuid),
	 (SELECT count(*) FROM employee_task_run WHERE task_id=$2::uuid),
	 (SELECT count(*) FROM employee_task_entry WHERE task_id=$2::uuid AND kind='resumed')`, f.request.Task.Scope.AgentID, f.request.Task.ID).Scan(&queues, &runs, &resumes)
	if err != nil || queues != wantQueues || runs != wantRuns || resumes != wantResumes {
		t.Fatalf("queues/runs/resumes = %d/%d/%d, want %d/%d/%d: %v", queues, runs, resumes, wantQueues, wantRuns, wantResumes, err)
	}
}

func TestContinueDirectTaskUsesOuterTransactionAndOneConnection(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL required")
	}
	u, err := url.Parse(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("pool_max_conns", "1")
	u.RawQuery = query.Encode()
	t.Setenv("DATABASE_URL", u.String())
	f, request, resume := completedDirectContinuation(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	builder := &stubOverlayBuilder{resp: json.RawMessage(`{"mcpServers":{"fixture":{"url":"https://test.invalid/session"}}}`)}
	f.service.Composio, f.service.FeatureFlags = builder, composioMCPAppsTestFlags(true)
	wakeup := &stubWakeup{}
	f.service.Wakeup = wakeup
	launcher := &stubRuntimeLauncher{calls: make(chan runtimeLaunchCall, 1)}
	f.service.RuntimeLauncher, f.service.runtimeLaunchLeases = launcher, newFakeRuntimeLaunchLeaseStore()
	f.service.RuntimeStartRecoveryConfig = func() RuntimeStartRecoveryConfig {
		return RuntimeStartRecoveryConfig{RecoverAbandonedLaunches: true}
	}
	prepared, err := f.service.PrepareDirectTask(ctx, request)
	if err != nil || builder.calls != 1 {
		t.Fatal("prepare failed to resolve overlay before transaction", builder.calls, err)
	}
	before := request.Task
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	rolledBack, err := f.service.ContinueDirectTaskTx(ctx, tx, prepared, resume)
	if err != nil {
		t.Fatal(err)
	}
	if !rolledBack.Created || len(wakeup.calls) != 0 || builder.calls != 1 || len(launcher.calls) != 0 {
		t.Fatal("continuation emitted an external effect inside transaction")
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	assertContinuationCounts(t, f, 1, 1, 0)
	after, err := employeetask.NewStore(f.pool).Get(ctx, before.Scope, before.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("rollback changed task snapshot", err)
	}
	result, err := continueDirectCommitted(ctx, f, prepared, resume)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.Run.TaskID != before.ID || result.Run.GoalRevision != before.GoalRevision || result.Task.IssueID.Valid || result.Task.AutopilotRunID.Valid || result.Task.ChatSessionID.Valid {
		t.Fatal("continuation changed task identity/revision or created a facade", result)
	}
	if result.Run.InputSeq != before.LastEntrySeq+1 || len(wakeup.calls) != 0 || builder.calls != 1 || len(launcher.calls) != 0 {
		t.Fatal("continuation lost input boundary or emitted an external effect", result.Run)
	}
	var storedOverlay, wantedOverlay any
	if err = json.Unmarshal(result.Task.RuntimeMcpOverlay, &storedOverlay); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(builder.resp, &wantedOverlay); err != nil || !reflect.DeepEqual(storedOverlay, wantedOverlay) {
		t.Fatal("continuation lost its prepared overlay", storedOverlay, err)
	}
	after, err = employeetask.NewStore(f.pool).Get(ctx, before.Scope, before.ID)
	if err != nil || !reflect.DeepEqual(after.Definition, before.Definition) || after.GoalRevision != before.GoalRevision || after.ActiveRunID != result.Run.ID || after.Version != before.Version+2 {
		t.Fatal("continuation changed the accepted goal", after, err)
	}
	var actor, body string
	if err = f.pool.QueryRow(ctx, `SELECT actor_ref,body FROM employee_task_entry WHERE task_id=$1::uuid AND kind='resumed'`, before.ID).Scan(&actor, &body); err != nil || actor != resume.ActorRef || body != resume.Body {
		t.Fatal("resume did not retain its source and actor", actor, body, err)
	}
	f.service.NotifyDirectTaskResult(ctx, result)
	if len(wakeup.calls) != 1 || wakeup.calls[0].taskID != util.UUIDToString(result.Task.ID) {
		t.Fatal("committed continuation was not notified", wakeup.calls)
	}
	select {
	case launch := <-launcher.calls:
		if launch.task.ID != result.Task.ID {
			t.Fatal("notification launched a different task", launch.task.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("committed continuation was not offered to the Runtime launcher")
	}
	if err = f.service.ShutdownRuntimeLaunches(ctx); err != nil {
		t.Fatal(err)
	}
	assertContinuationCounts(t, f, 2, 2, 1)
}

func TestContinueDirectTaskFailureCanBeJournaledWithoutPartialWrites(t *testing.T) {
	for _, failure := range []string{"runtime_capability", "run_source_conflict"} {
		t.Run(failure, func(t *testing.T) {
			f, request, resume := completedDirectContinuation(t)
			ctx := context.Background()
			if failure == "run_source_conflict" {
				request.Source = resume.Source
			}
			prepared, err := f.service.PrepareDirectTask(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "runtime_capability" {
				if _, err = f.pool.Exec(ctx, `UPDATE agent_runtime SET metadata='{}' WHERE id=(SELECT runtime_id FROM agent WHERE id=$1::uuid)`, request.Task.Scope.AgentID); err != nil {
					t.Fatal(err)
				}
			}
			wakeup := &stubWakeup{}
			f.service.Wakeup = wakeup
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err = f.service.ContinueDirectTaskTx(ctx, tx, prepared, resume); err == nil {
				t.Fatal("continuation accepted invalid runtime/source")
			}
			// The Host persists the failure journal in this transaction. It may
			// commit after a business error, but no partial execution may survive.
			if err = tx.Commit(ctx); err != nil {
				t.Fatal("continuation left the outer transaction unusable", err)
			}
			assertContinuationCounts(t, f, 1, 1, 0)
			after, err := employeetask.NewStore(f.pool).Get(ctx, request.Task.Scope, request.Task.ID)
			if err != nil || !reflect.DeepEqual(after, request.Task) || len(wakeup.calls) != 0 {
				t.Fatal("failed continuation changed the task or notified", after, err)
			}
		})
	}
}

func TestContinueDirectTaskReplaysRunningAndCompletedRun(t *testing.T) {
	f, request, resume := completedDirectContinuation(t)
	ctx := context.Background()
	prepared, err := f.service.PrepareDirectTask(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	first, err := continueDirectCommitted(ctx, f, prepared, resume)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"queued", "running", "completed"} {
		t.Run(status, func(t *testing.T) {
			if status == "completed" {
				completeDirectContinuation(t, f, first)
			} else if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET status=$2 WHERE id=$1`, first.Task.ID, status); err != nil {
				t.Fatal(err)
			}
			wakeup := &stubWakeup{}
			f.service.Wakeup = wakeup
			replay, err := continueDirectCommitted(ctx, f, prepared, resume)
			if err != nil || replay.Created || replay.Task.ID != first.Task.ID || replay.Run.ID != first.Run.ID || replay.Task.Status != status {
				t.Fatal("exact continuation replay did not recover accepted execution", replay, err)
			}
			f.service.NotifyDirectTaskResult(ctx, replay)
			want := 0
			if status == "queued" {
				want = 1
			}
			if len(wakeup.calls) != want {
				t.Fatal("replay woke an ineligible queue", wakeup.calls)
			}
		})
	}
	assertContinuationCounts(t, f, 2, 2, 1)
}

func TestContinueDirectTaskRejectsChangedReplay(t *testing.T) {
	f, request, resume := completedDirectContinuation(t)
	ctx := context.Background()
	prepared, err := f.service.PrepareDirectTask(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = continueDirectCommitted(ctx, f, prepared, resume); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"prompt", "scope", "principal", "requester", "owner", "resume_body", "resume_actor"} {
		t.Run(field, func(t *testing.T) {
			changed, changedResume := request, resume
			switch field {
			case "prompt":
				changed.Prompt = "different"
			case "scope":
				changed.Task.Scope.Scene.SceneID = uuid.NewString()
			case "principal":
				changed.PrincipalID = pgtype.UUID{Bytes: uuid.New(), Valid: true}
			case "requester":
				changed.Task.RequesterRef = "member:" + uuid.NewString()
			case "owner":
				changed.Task.OwnerLoop = employeetask.LoopCoordinator
			case "resume_body":
				changedResume.Body = `{"source":"changed"}`
			case "resume_actor":
				changedResume.ActorRef = "member:" + uuid.NewString()
			}
			// A prepared input is immutable; each changed host input is prepared again.
			f.service.Composio = nil
			altered, err := f.service.PrepareDirectTask(ctx, changed)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = continueDirectCommitted(ctx, f, altered, changedResume); !errors.Is(err, employeetask.ErrConflict) {
				t.Fatal("changed replay was not rejected as a conflict", err)
			}
		})
	}
	assertContinuationCounts(t, f, 2, 2, 1)
}

func TestContinueDirectTaskConcurrentCAS(t *testing.T) {
	f, request, resume := completedDirectContinuation(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, call := range []string{"one", "two"} {
		p, r := request, resume
		p.Source.Key, r.Source.Key = "receipt/"+call+"/run", "receipt/"+call+"/resume"
		prepared, err := f.service.PrepareDirectTask(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		wg.Go(func() {
			_, err := continueDirectCommitted(ctx, f, prepared, r)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	winners, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			winners++
		} else if errors.Is(err, employeetask.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatal("continuation CAS admitted concurrent writers", winners, conflicts)
	}
	assertContinuationCounts(t, f, 2, 2, 1)
}

func TestContinueDirectTaskConcurrentSourceReplay(t *testing.T) {
	f, request, resume := completedDirectContinuation(t)
	ctx := context.Background()
	prepared, err := f.service.PrepareDirectTask(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan DirectTaskResult, 6)
	errs := make(chan error, 6)
	for range 6 {
		wg.Go(func() {
			result, err := continueDirectCommitted(ctx, f, prepared, resume)
			results <- result
			errs <- err
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first DirectTaskResult
	created := 0
	for result := range results {
		if first.Run.ID == "" {
			first = result
		}
		if result.Run.ID != first.Run.ID || result.Task.ID != first.Task.ID {
			t.Fatal("concurrent replay created another continuation")
		}
		if result.Created {
			created++
		}
	}
	if created != 1 {
		t.Fatal("concurrent replay repeated created analytics", created)
	}
	assertContinuationCounts(t, f, 2, 2, 1)
}

func TestContinueDirectTaskRechecksCurrentAccess(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "replay"}[replay], func(t *testing.T) {
			f, request, resume := completedDirectContinuation(t)
			ctx := context.Background()
			prepared, err := f.service.PrepareDirectTask(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			wantQueues, wantResumes := 1, 0
			if replay {
				if _, err = continueDirectCommitted(ctx, f, prepared, resume); err != nil {
					t.Fatal(err)
				}
				wantQueues, wantResumes = 2, 1
			}
			if _, err = f.pool.Exec(ctx, `DELETE FROM member WHERE workspace_id=$1::uuid AND user_id=$2`, request.Task.Scope.WorkspaceID, request.PrincipalID); err != nil {
				t.Fatal(err)
			}
			if _, err = continueDirectCommitted(ctx, f, prepared, resume); !errors.Is(err, ErrDirectTaskAccessDenied) {
				t.Fatal("prepared authorization outlived current membership", err)
			}
			assertContinuationCounts(t, f, wantQueues, wantQueues, wantResumes)
		})
	}
}

func TestContinueDirectTaskRejectsUnfinishedOrFailedTask(t *testing.T) {
	for _, state := range []string{"running", "failed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			f := directDatabase(t)
			ctx := context.Background()
			first, err := f.service.EnqueueDirectTask(ctx, f.request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1`, first.Task.ID); err != nil {
				t.Fatal(err)
			}
			want := employeetask.ErrRunNotReady
			switch state {
			case "running":
				want = employeetask.ErrActiveRun
			case "failed":
				_, err = f.service.FailTask(ctx, first.Task.ID, "fixture failure", "", "", "agent_error", false, "")
			case "cancelled":
				_, err = f.service.CancelTask(ctx, first.Task.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			request := f.request
			request.Task, err = employeetask.NewStore(f.pool).Get(ctx, request.Task.Scope, request.Task.ID)
			if err != nil || string(request.Task.State) != state {
				t.Fatal("fixture did not reach requested state", request.Task.State, err)
			}
			request.Source = employeetask.Source{Namespace: "employee_scene", Key: "receipt/call/run"}
			resume := employeetask.ResumeParams{Source: employeetask.Source{Namespace: "employee_scene", Key: "receipt/call/resume"}, ActorRef: request.Task.RequesterRef, Body: `{"source":"continue"}`, ExpectedVersion: request.Task.Version}
			prepared, err := f.service.PrepareDirectTask(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = continueDirectCommitted(ctx, f, prepared, resume); !errors.Is(err, want) {
				t.Fatal("unsafe continuation accepted", err)
			}
			assertContinuationCounts(t, f, 1, 1, 0)
		})
	}
}
