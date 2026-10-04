package employeetask

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestReopenGoalRequiresCompletedOwnerAndPreservesReplay(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	ready := createGoal(t, f, "uncompleted")
	request := ResumeParams{Source: Source{"human", "reopen"}, ActorRef: ready.RequesterRef, Body: "一周的，给我一个表格。", ExpectedVersion: ready.Version}
	if _, _, err := f.store.ReopenGoal(ctx, ready.Scope, ready.ID, request); !errors.Is(err, ErrConflict) {
		t.Fatal("unfinished Goal reopened", err)
	}
	task, run := runToTerminal(t, f, ready, "first-run", StateSucceeded)
	complete := completion(task, "confirmed")
	complete.EvidenceRef = "run:" + run.ID
	task, _, err := f.store.CompleteGoal(ctx, task.Scope, task.ID, complete)
	if err != nil {
		t.Fatal(err)
	}
	request.ExpectedVersion = task.Version
	other := request
	other.ActorRef = "other-person"
	if _, _, err := f.store.ReopenGoal(ctx, task.Scope, task.ID, other); !errors.Is(err, ErrConflict) {
		t.Fatal("other requester reopened Goal", err)
	}
	reopened, entry, err := f.store.ReopenGoal(ctx, task.Scope, task.ID, request)
	if err != nil || reopened.State != StateReady || reopened.GoalRevision != task.GoalRevision+1 || !reflect.DeepEqual(reopened.Definition, task.Definition) || entry.Kind != "resumed" || entry.ActorRef != task.RequesterRef {
		t.Fatal("completed continuation changed constraints or lost its new revision", reopened, entry, err)
	}
	again, repeated, err := f.store.ReopenGoal(ctx, task.Scope, task.ID, request)
	if err != nil || again.GoalRevision != reopened.GoalRevision || repeated.Seq != entry.Seq {
		t.Fatal("source replay reopened twice", again, repeated, err)
	}
	changed := request
	changed.Body = "changed requested work"
	if _, _, err := f.store.ReopenGoal(ctx, task.Scope, task.ID, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed source replay accepted", err)
	}
	stopped, _, err := f.store.Stop(ctx, task.Scope, task.ID, StopParams{Source: Source{"human", "stop"}, ActorRef: task.RequesterRef, Body: "stop", RunID: run.ID, QueueTaskID: run.QueueTaskID, ExpectedVersion: reopened.Version})
	if err != nil {
		t.Fatal(err)
	}
	request.Source.Key = "after-stop"
	request.ExpectedVersion = stopped.Version
	if _, _, err := f.store.ReopenGoal(ctx, task.Scope, task.ID, request); err == nil {
		t.Fatal("human stop was reopened")
	}
}
