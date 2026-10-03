package employeetask

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestStopClosesGoalWithoutRewritingRunFacts(t *testing.T) {
	for _, state := range []State{StateReady, StateRunning, StateSucceeded, StateFailed} {
		t.Run(string(state), func(t *testing.T) {
			f := database(t)
			ctx := context.Background()
			task := createTask(t, f)
			var run Run
			var err error
			if state != StateReady {
				run, err = f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"test", "run"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version})
				if err != nil {
					t.Fatal(err)
				}
				if state != StateRunning {
					if _, run, err = f.store.RecordResult(ctx, task.Scope, task.ID, ResultParams{Source: Source{"test", "result"}, RunID: run.ID, State: state, Result: "original fact"}); err != nil {
						t.Fatal(err)
					}
				}
				task, err = f.store.Get(ctx, task.Scope, task.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			p := StopParams{Source: Source{"employee_scene_stop", "receipt/call"}, ActorRef: task.RequesterRef, Body: `{"text":"stop this task"}`, RunID: run.ID, QueueTaskID: run.QueueTaskID, ExpectedVersion: task.Version}
			stopped, entry, err := f.store.Stop(ctx, task.Scope, task.ID, p)
			if err != nil || stopped.State != StateCancelled || stopped.ActiveRunID != "" || entry.Kind != "input" || entry.RunID != run.ID || entry.Body != p.Body || stopped.GoalRevision != task.GoalRevision {
				t.Fatal("stop did not close the goal independently of its Run", stopped, entry, err)
			}
			var operation string
			if err := f.pool.QueryRow(ctx, `SELECT payload->>'operation' FROM employee_task_entry WHERE task_id=$1::uuid AND seq=$2`, task.ID, entry.Seq).Scan(&operation); err != nil || operation != "stop" {
				t.Fatal("stop intent is not typed", operation, err)
			}
			if run.ID != "" {
				got, err := f.store.LatestRun(ctx, task.Scope, task.ID)
				if err != nil || got.State != run.State || got.Result != run.Result {
					t.Fatal("domain stop rewrote execution facts", got, err)
				}
			}
			replay, same, err := f.store.Stop(ctx, task.Scope, task.ID, p)
			if err != nil || replay.Version != stopped.Version || same.Seq != entry.Seq {
				t.Fatal("source replay appended stop", replay, same, err)
			}
			p.Body = "changed stop payload"
			if _, _, err := f.store.Stop(ctx, task.Scope, task.ID, p); !errors.Is(err, ErrConflict) {
				t.Fatal("changed source replay accepted", err)
			}
			if _, _, err := f.store.Steer(ctx, task.Scope, task.ID, SteerParams{Source: Source{"test", "steer"}, ActorRef: task.RequesterRef, Body: "try to restart"}); !errors.Is(err, ErrStopped) && !errors.Is(err, ErrNotFound) {
				t.Fatal("ordinary correction reopened stop", err)
			}
			if _, _, err := f.store.Resume(ctx, task.Scope, task.ID, ResumeParams{Source: Source{"test", "resume"}, ActorRef: task.RequesterRef, Body: "try to continue", ExpectedVersion: stopped.Version}); err == nil {
				t.Fatal("continuation reopened stop")
			}
		})
	}
}
