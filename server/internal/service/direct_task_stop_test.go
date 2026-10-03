package service

import (
	"context"
	"errors"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/util"
)

func directStopFixture(t *testing.T, status string) (directFixture, DirectTaskStopRequest) {
	t.Helper()
	f := directDatabase(t)
	ctx := context.Background()
	var first DirectTaskResult
	var err error
	if status != "none" {
		first, err = f.service.EnqueueDirectTask(ctx, f.request)
		if err != nil {
			t.Fatal(err)
		}
		if status != "queued" {
			if _, err = f.pool.Exec(ctx, `UPDATE agent_task_queue SET status='running',dispatched_at=now(),started_at=now() WHERE id=$1`, first.Task.ID); err != nil {
				t.Fatal(err)
			}
			switch status {
			case "completed":
				_, err = f.service.CompleteTask(ctx, first.Task.ID, []byte(`{"output":"original result"}`), "", "", false, "")
			case "failed":
				_, err = f.service.FailTask(ctx, first.Task.ID, "original failure", "", "", "agent_error", false, "")
			case "cancelled":
				_, err = f.service.CancelTask(ctx, first.Task.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	task, err := employeetask.NewStore(f.pool).Get(ctx, f.request.Task.Scope, f.request.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return f, DirectTaskStopRequest{Task: task, RunID: first.Run.ID, QueueTaskID: first.Run.QueueTaskID, PrincipalID: f.request.PrincipalID, Source: employeetask.Source{Namespace: "employee_scene_stop", Key: uuid.NewString() + "/stop-call"}, ActorRef: task.RequesterRef, Body: `{"message":{"text":"stop this task now"}}`}
}

func stopDirectCommitted(ctx context.Context, f directFixture, req DirectTaskStopRequest) (DirectTaskStopResult, error) {
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		return DirectTaskStopResult{}, err
	}
	defer tx.Rollback(ctx)
	result, err := f.service.StopDirectTaskTx(ctx, tx, req)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func TestStopDirectTaskPreservesFactsAndWaitsForExit(t *testing.T) {
	for _, tc := range []struct {
		before, state string
		exited        bool
	}{{"none", "stopped", true}, {"queued", "stopped", true}, {"running", "stopping", false}, {"completed", "completed", false}, {"failed", "failed", false}, {"cancelled", "unconfirmed", false}} {
		t.Run(tc.before, func(t *testing.T) {
			f, req := directStopFixture(t, tc.before)
			ctx := context.Background()
			got, err := stopDirectCommitted(ctx, f, req)
			if err != nil || got.State != tc.state || got.ExitConfirmed != tc.exited || got.Task.State != employeetask.StateCancelled || !got.Changed || got.Entry.RunID != req.RunID {
				t.Fatal("stop state lost execution facts or exit uncertainty", got, err)
			}
			if tc.before == "completed" && got.Queue.Status != "completed" || tc.before == "failed" && got.Queue.Status != "failed" {
				t.Fatal("stop rewrote terminal queue facts", got.Queue.Status)
			}
			var queues, runs int
			if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM agent_task_queue WHERE agent_id=$1::uuid),(SELECT count(*) FROM employee_task_run WHERE task_id=$2::uuid)`, req.Task.Scope.AgentID, req.Task.ID).Scan(&queues, &runs); err != nil || queues != runs || queues > 1 {
				t.Fatal("stop admitted another writer", queues, runs, err)
			}
			if tc.before == "running" {
				if err := f.service.AcknowledgeTaskProcessStopped(ctx, got.Queue.ID); err != nil {
					t.Fatal(err)
				}
				replay, err := stopDirectCommitted(ctx, f, req)
				if err != nil || replay.State != "stopped" || !replay.ExitConfirmed || replay.Changed || !replay.Replayed {
					t.Fatal("ACK did not settle exact stopped execution", replay, err)
				}
				_, _ = f.service.CompleteTask(ctx, got.Queue.ID, []byte(`{"output":"late result"}`), "", "", false, "")
				queue, err := f.service.Queries.GetAgentTask(ctx, got.Queue.ID)
				task, taskErr := employeetask.NewStore(f.pool).Get(ctx, req.Task.Scope, req.Task.ID)
				if err != nil || taskErr != nil || queue.Status != "cancelled" || task.State != employeetask.StateCancelled {
					t.Fatal("late completion reopened stop", queue.Status, task.State, err, taskErr)
				}
			}
		})
	}
}

func TestStopDirectTaskCompletedNeedsProcessExitEvidence(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		name := "completed_without_exit_receipt"
		if confirmed {
			name = "completed_with_host_exit_receipt"
		}
		t.Run(name, func(t *testing.T) {
			f, req := directStopFixture(t, "completed")
			ctx := context.Background()
			if confirmed {
				if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET context=context || jsonb_build_object('process_stopped_at',now()) WHERE id=$1::uuid`, req.QueueTaskID); err != nil {
					t.Fatal(err)
				}
			}
			result, err := stopDirectCommitted(ctx, f, req)
			if err != nil || result.State != "completed" || result.ExitConfirmed != confirmed || result.Run.State != employeetask.StateSucceeded || result.Queue.Status != "completed" || result.QueueChanged {
				t.Fatalf("completion confused with process-group exit: state=%s exit=%t run=%s queue=%s changed=%t err=%v", result.State, result.ExitConfirmed, result.Run.State, result.Queue.Status, result.QueueChanged, err)
			}
		})
	}
}

func TestStopDirectTaskCompletedPredecessorIsNotAStopObligation(t *testing.T) {
	for _, status := range []string{"queued", "running"} {
		t.Run(status, func(t *testing.T) {
			f := newDirectSteerFixture(t)
			ctx := context.Background()
			completeDirectContinuation(t, f.directFixture, f.first)
			next, err := f.steer("after-completion", "apply the current correction")
			if err != nil {
				t.Fatal(err)
			}
			if status == "running" {
				f.claim(t, next.Queue, "new-writer")
			}
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			state, err := f.service.ReadDirectTaskExecutionState(ctx, tx, next.Task.Scope, next.Task.ID)
			_ = tx.Rollback(ctx)
			if err != nil || state.State != status || state.PendingPredecessor {
				t.Fatal("historical completion became a new stop obligation", state, err)
			}
			req := DirectTaskStopRequest{Task: next.Task, RunID: next.Run.ID, QueueTaskID: next.Run.QueueTaskID, PrincipalID: f.request.PrincipalID, Source: employeetask.Source{Namespace: "employee_scene_stop", Key: "current/stop"}, ActorRef: next.Task.RequesterRef, Body: `{"text":"stop the current task"}`}
			stopped, err := stopDirectCommitted(ctx, f.directFixture, req)
			want := "stopped"
			if status == "running" {
				want = "stopping"
			}
			if err != nil || stopped.State != want || stopped.PendingPredecessor || stopped.ExitConfirmed != (status == "queued") {
				t.Fatalf("stop extended beyond its current Run and cancelled predecessors: state=%s exit=%t predecessor=%t err=%v", stopped.State, stopped.ExitConfirmed, stopped.PendingPredecessor, err)
			}
		})
	}
}

func TestStopDirectTaskConcurrentReplayAndCAS(t *testing.T) {
	f, req := directStopFixture(t, "running")
	ctx := context.Background()
	var wg sync.WaitGroup
	out := make(chan DirectTaskStopResult, 6)
	errs := make(chan error, 6)
	for range 6 {
		wg.Go(func() { got, err := stopDirectCommitted(ctx, f, req); out <- got; errs <- err })
	}
	wg.Wait()
	close(out)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	changed := 0
	for got := range out {
		if got.Changed {
			changed++
		}
		if got.Run.ID != req.RunID || got.Queue.ID != util.MustParseUUID(req.QueueTaskID) || got.State != "stopping" {
			t.Fatal("replay retargeted stop", got)
		}
	}
	if changed != 1 {
		t.Fatal("duplicate durable stop intent", changed)
	}
	bad := req
	bad.Body = "changed request"
	if _, err := stopDirectCommitted(ctx, f, bad); !errors.Is(err, employeetask.ErrConflict) {
		t.Fatal("changed source replay accepted", err)
	}
	bad = req
	bad.Source.Key = "new-stale-stop"
	if _, err := stopDirectCommitted(ctx, f, bad); !errors.Is(err, employeetask.ErrConflict) {
		t.Fatal("stale Task version accepted", err)
	}
	bad.Task, _ = employeetask.NewStore(f.pool).Get(ctx, req.Task.Scope, req.Task.ID)
	got, err := stopDirectCommitted(ctx, f, bad)
	if err != nil || !got.Changed || got.QueueChanged || got.State != "stopping" {
		t.Fatal("second explicit stop repeated queue cancellation", got, err)
	}
}

func TestStopDirectTaskSavepointAndOneConnection(t *testing.T) {
	u, err := url.Parse(os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("pool_max_conns", "1")
	u.RawQuery = q.Encode()
	t.Setenv("DATABASE_URL", u.String())
	f, req := directStopFixture(t, "running")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wakeup := &stubWakeup{}
	f.service.Wakeup = wakeup
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.service.StopDirectTaskTx(ctx, tx, req)
	if err != nil || got.State != "stopping" || len(wakeup.calls) != 0 {
		t.Fatal("stop used pool or notified inside outer transaction", got, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := employeetask.NewStore(f.pool).Get(ctx, req.Task.Scope, req.Task.ID)
	if err != nil || !reflect.DeepEqual(req.Task, after) {
		t.Fatal("outer rollback leaked goal stop", after, err)
	}
	name := "stop_reject_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION `+name+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id='`+req.QueueTaskID+`'::uuid AND NEW.status='cancelled' THEN RAISE EXCEPTION 'stop test failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER `+name+` BEFORE UPDATE ON agent_task_queue FOR EACH ROW EXECUTE FUNCTION `+name+`() `); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DROP TRIGGER `+name+` ON agent_task_queue; DROP FUNCTION `+name+`()`)
	})
	tx, err = f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := f.service.StopDirectTaskTx(ctx, tx, req); err == nil {
		t.Fatal("forced queue failure was accepted")
	}
	var usable bool
	if err := tx.QueryRow(ctx, `SELECT true`).Scan(&usable); err != nil || !usable {
		t.Fatal("business failure poisoned Host journal transaction", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	after, err = employeetask.NewStore(f.pool).Get(ctx, req.Task.Scope, req.Task.ID)
	if err != nil || !reflect.DeepEqual(req.Task, after) {
		t.Fatal("failed stop partially committed", after, err)
	}
}

func TestStopDirectTaskIncludesPendingSteerPredecessor(t *testing.T) {
	f := newDirectSteerFixture(t)
	ctx := context.Background()
	f.claim(t, f.first.Task, "old-writer")
	next, err := f.steer("correction", "change output")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := f.service.ReconcileEmployeeTaskStops(ctx, 100); err != nil || n != 0 {
		t.Fatal("stop recovery adopted a steer with no stop intent", n, err)
	}
	task, err := employeetask.NewStore(f.pool).Get(ctx, f.request.Task.Scope, f.request.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	req := DirectTaskStopRequest{Task: task, RunID: next.Run.ID, QueueTaskID: next.Run.QueueTaskID, PrincipalID: f.request.PrincipalID, Source: employeetask.Source{Namespace: "employee_scene_stop", Key: "receipt/stop"}, ActorRef: task.RequesterRef, Body: `{"text":"stop"}`}
	got, err := stopDirectCommitted(ctx, f.directFixture, req)
	if err != nil || got.State != "stopping" || got.ExitConfirmed || !got.PendingPredecessor || taskProcessStopPending(got.Queue) {
		t.Fatal("unclaimed successor hid its live predecessor", got, err)
	}
	launcher := &terminalRecordingLauncher{}
	f.service.RuntimeLauncher = launcher
	if n, err := f.service.ReconcileEmployeeTaskStops(ctx, 100); err != nil || n != 1 {
		t.Fatal("stop recovery did not find predecessor", n, err)
	}
	found := false
	for _, q := range launcher.terminal {
		if q.ID == f.first.Task.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("recovery notified cancelled successor instead of pending predecessor")
	}
	if err := f.service.AcknowledgeTaskProcessStopped(ctx, f.first.Task.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := f.service.ReadDirectTaskExecutionState(ctx, tx, task.Scope, task.ID)
	_ = tx.Rollback(ctx)
	if err != nil || projection.State != "stopped" || !projection.ExitConfirmed || projection.PendingPredecessor || !projection.StopRequested {
		t.Fatal("predecessor ACK did not settle stop", projection, err)
	}
	if n, err := f.service.ReconcileEmployeeTaskStops(ctx, 100); err != nil || n != 0 {
		t.Fatal("settled stop kept rearming observers", n, err)
	}
}

func TestStopDirectTaskRejectsUnboundAuthorityWithoutMutation(t *testing.T) {
	for _, change := range []string{"requester", "principal", "scope", "run", "queue", "archived_agent"} {
		t.Run(change, func(t *testing.T) {
			f, req := directStopFixture(t, "running")
			ctx := context.Background()
			original := req
			switch change {
			case "requester":
				req.ActorRef = "another-requester"
			case "principal":
				req.PrincipalID = util.MustParseUUID(uuid.NewString())
			case "scope":
				req.Task.Scope.TenantOrgID = "another-org"
			case "run":
				req.RunID = uuid.NewString()
			case "queue":
				req.QueueTaskID = uuid.NewString()
			case "archived_agent":
				if _, err := f.pool.Exec(ctx, `UPDATE agent SET archived_at=now() WHERE id=$1::uuid`, req.Task.Scope.AgentID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := stopDirectCommitted(ctx, f, req); err == nil {
				t.Fatal("unbound stop was accepted")
			}
			queue, err := f.service.Queries.GetAgentTask(ctx, util.MustParseUUID(original.QueueTaskID))
			task, taskErr := employeetask.NewStore(f.pool).Get(ctx, original.Task.Scope, original.Task.ID)
			if err != nil || taskErr != nil || queue.Status != "running" || task.State != employeetask.StateRunning || task.Version != original.Task.Version {
				t.Fatal("rejected stop changed execution", queue.Status, task, err, taskErr)
			}
		})
	}
}
