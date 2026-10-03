package employeetask

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// An interrupted Direct Run blocks every successor until the host records
// fence evidence; a steer alone never opens the writer fence.
func TestSteerInterruptRequiresWriterFence(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := createTask(t, f)
	run, err := f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"host", "first"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.store.Steer(ctx, task.Scope, task.ID, SteerParams{Source: Source{"steer", "early"}, ActorRef: "human:a", Body: "use the signed date"}); !errors.Is(err, ErrActiveRun) {
		t.Fatalf("steer without merge must not bypass an active run: %v", err)
	}
	if _, _, err = f.store.RecordResult(ctx, task.Scope, task.ID, ResultParams{Source: Source{"queue_terminal", run.QueueTaskID}, RunID: run.ID, State: StateCancelled, Result: "task cancelled"}); err != nil {
		t.Fatal(err)
	}
	steered, entry, err := f.store.Steer(ctx, task.Scope, task.ID, SteerParams{Source: Source{"steer", "c1"}, ActorRef: "human:a", Body: "use the signed date"})
	if err != nil || steered.State != StateReady || entry.Kind != "steer" || entry.RunID != "" {
		t.Fatalf("steer: %+v %+v %v", steered, entry, err)
	}
	if _, err = f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"steer", "c1/run"}, QueueTaskID: uuid.NewString(), ExpectedVersion: steered.Version}); !errors.Is(err, ErrRunNotReady) {
		t.Fatalf("unfenced writer must block the successor: %v", err)
	}
	if _, _, err = f.store.FenceRunWriter(ctx, task.Scope, task.ID, FenceWriterParams{Source: Source{"fence", run.ID}, RunID: run.ID, Evidence: "trust me"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown evidence accepted: %v", err)
	}
	fenced, fence, err := f.store.FenceRunWriter(ctx, task.Scope, task.ID, FenceWriterParams{Source: Source{"fence", run.ID}, RunID: run.ID, Evidence: FenceClaimBarrier})
	if err != nil || fence.Kind != "writer_fenced" || fence.RunID != run.ID {
		t.Fatalf("fence: %+v %v", fence, err)
	}
	successor, err := f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"steer", "c1/run"}, QueueTaskID: uuid.NewString(), ExpectedVersion: fenced.Version})
	if err != nil || successor.InputSeq != fenced.LastEntrySeq {
		t.Fatalf("successor: %+v %v", successor, err)
	}
	found, err := f.store.RunBySource(ctx, task.Scope, task.ID, Source{"steer", "c1/run"})
	if err != nil || found.ID != successor.ID {
		t.Fatalf("run by source: %+v %v", found, err)
	}
	if _, _, err = f.store.FenceRunWriter(ctx, task.Scope, task.ID, FenceWriterParams{Source: Source{"fence", successor.ID}, RunID: successor.ID, Evidence: FenceProcessStopped}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a running writer cannot be fenced: %v", err)
	}
}

// A correction that arrives before the successor is claimed joins that Run:
// its input boundary moves, no second Run exists, and replays are no-ops.
func TestSteerMergesIntoUnclaimedRun(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := createTask(t, f)
	run, err := f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"host", "first"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	p := SteerParams{Source: Source{"steer", "m1"}, ActorRef: "human:a", Body: "exclude test customers", MergeRunID: run.ID}
	merged, entry, err := f.store.Steer(ctx, task.Scope, task.ID, p)
	if err != nil || merged.State != StateRunning || merged.ActiveRunID != run.ID || entry.RunID != run.ID {
		t.Fatalf("merge: %+v %+v %v", merged, entry, err)
	}
	current, err := f.store.getRun(ctx, task.Scope, task.ID, run.ID)
	if err != nil || current.InputSeq != entry.Seq {
		t.Fatalf("merged run input boundary: %+v %v", current, err)
	}
	again, replay, err := f.store.Steer(ctx, task.Scope, task.ID, p)
	if err != nil || again.Version != merged.Version || replay.Seq != entry.Seq {
		t.Fatalf("replay: %+v %+v %v", again, replay, err)
	}
	changed := p
	changed.Body = "a different correction"
	if _, _, err = f.store.Steer(ctx, task.Scope, task.ID, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same source, different payload: %v", err)
	}
	if _, _, err = f.store.Steer(ctx, task.Scope, task.ID, SteerParams{Source: Source{"steer", "m2"}, ActorRef: "human:a", Body: "stale", MergeRunID: uuid.NewString()}); !errors.Is(err, ErrConflict) {
		t.Fatalf("merge into a non-active run: %v", err)
	}
	if _, _, err = f.store.Steer(ctx, task.Scope, task.ID, SteerParams{Source: Source{"steer", "m3"}, ActorRef: "human:b", Body: "second correction", MergeRunID: run.ID}); err != nil {
		t.Fatal(err)
	}
	corrections, err := f.store.Corrections(ctx, task.Scope, task.ID, 10)
	if err != nil || len(corrections) != 2 || corrections[0].Body != "exclude test customers" || corrections[1].Body != "second correction" {
		t.Fatalf("corrections: %+v %v", corrections, err)
	}
	if latest, err := f.store.LatestRun(ctx, task.Scope, task.ID); err != nil || latest.ID != run.ID {
		t.Fatalf("latest run: %+v %v", latest, err)
	}
}

// A finished task can be steered: it reopens without pretending a failed
// writer exited, and a succeeded task needs no fence.
func TestSteerContinuesFinishedTask(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := createTask(t, f)
	run, err := f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"host", "first"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.store.RecordResult(ctx, task.Scope, task.ID, ResultParams{Source: Source{"queue_terminal", run.QueueTaskID}, RunID: run.ID, State: StateSucceeded, Result: "draft"}); err != nil {
		t.Fatal(err)
	}
	steered, _, err := f.store.Steer(ctx, task.Scope, task.ID, SteerParams{Source: Source{"steer", "after"}, ActorRef: "human:a", Body: "also add totals"})
	if err != nil || steered.State != StateReady {
		t.Fatalf("steer finished: %+v %v", steered, err)
	}
	if _, err = f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"steer", "after/run"}, QueueTaskID: uuid.NewString(), ExpectedVersion: steered.Version}); err != nil {
		t.Fatalf("continuation after success: %v", err)
	}
}

func TestWithCorrectionsFollowsCompilerPosition(t *testing.T) {
	base := "Work packet:\n- Scope: x\n- Principal: p\n- DEFINITION (the contract you execute against):\n  Goal: g"
	out := WithCorrections(base, []SteerCorrection{{Ref: "employee_task_entry:t/4", ActorRef: "human:a", Body: "use the signed date"}})
	lines := strings.Split(out, "\n")
	if lines[2] != "- Principal: p" || !strings.HasPrefix(lines[3], "- CURRENT CORRECTIONS") || !strings.Contains(out, "use the signed date") {
		t.Fatalf("corrections block misplaced:\n%s", out)
	}
	if strings.Index(out, "use the signed date") > strings.Index(out, "- DEFINITION") {
		t.Fatal("corrections must precede the definition")
	}
	if WithCorrections(base, nil) != base {
		t.Fatal("no corrections must leave the packet unchanged")
	}
}
