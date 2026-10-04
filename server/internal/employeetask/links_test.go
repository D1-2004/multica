package employeetask

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestCompileUpstreamResultsAreBoundedAndRecorded(t *testing.T) {
	input := compilerFixture()
	upstream := func(id, body string) PacketMaterial {
		return PacketMaterial{Ref: "upstream:" + id, Scope: input.Scope, PrincipalID: input.PrincipalID, Body: body}
	}
	first, second := uuid.NewString(), uuid.NewString()
	input.Upstream = []PacketMaterial{upstream(first, "Bug count last week: 42"), upstream(second, "")}
	packet, err := Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(packet.Text, "UPSTREAM RESULTS") || !strings.Contains(packet.Text, "Bug count last week: 42") {
		t.Fatalf("upstream result missing:\n%s", packet.Text)
	}
	want := []string{"message:correction", "message:source", "run:completed", "upstream:" + first, "document:ledger", "message:history"}
	if !reflect.DeepEqual(packet.ContextUsed, want) {
		t.Fatalf("manifest=%v want %v (an empty upstream body is not used context)", packet.ContextUsed, want)
	}
	for name, mutate := range map[string]func(*CompileInput){
		"too many":     func(c *CompileInput) { c.Upstream = append(c.Upstream, upstream(uuid.NewString(), "a"), upstream(uuid.NewString(), "b")) },
		"oversized":    func(c *CompileInput) { c.Upstream[0].Body = strings.Repeat("x", MaxUpstreamReportBytes+1) },
		"foreign ref":  func(c *CompileInput) { c.Upstream[0].Ref = "document:" + first },
		"bad task id":  func(c *CompileInput) { c.Upstream[0].Ref = "upstream:t1" },
		"cross scope":  func(c *CompileInput) { c.Upstream[0].Scope.TenantOrgID = "org-b" },
		"cross person": func(c *CompileInput) { c.Upstream[0].PrincipalID = uuid.NewString() },
	} {
		bad := input
		bad.Upstream = append([]PacketMaterial(nil), input.Upstream...)
		mutate(&bad)
		if _, err := Compile(bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}

func TestCreateBuildsOnLinksSameScopeAndRequester(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	upstream := createTask(t, f)
	upstream, _ = runToTerminal(t, f, upstream, "count", StateSucceeded)
	p := f.params
	p.Source.Key = "event-2/action-1"
	p.Definition.Goal = "Write the retrospective from the bug count"
	p.BuildsOn = []string{upstream.ID}
	down, err := f.store.Create(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	links, err := f.store.Links(ctx, down.Scope, down.ID)
	if err != nil || len(links.BuildsOn) != 1 || links.BuildsOn[0].RelatedTaskID != upstream.ID || links.BuildsOn[0].EntrySeq != 1 || len(links.BlockedBy) != 0 {
		t.Fatalf("links: %+v %v", links, err)
	}
	upLinks, err := f.store.Links(ctx, upstream.Scope, upstream.ID)
	if err != nil || len(upLinks.Dependents) != 1 || upLinks.Dependents[0].TaskID != down.ID {
		t.Fatalf("dependents: %+v %v", upLinks, err)
	}
	again, err := f.store.Create(ctx, p)
	if err != nil || again.ID != down.ID {
		t.Fatalf("replay: %+v %v", again, err)
	}
	changed := p
	changed.BuildsOn = nil
	if _, err = f.store.Create(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same source without builds_on replayed: %v", err)
	}
	var n int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM employee_task_link WHERE task_id=$1::uuid`, down.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("replay duplicated links: %d %v", n, err)
	}

	// Another requester's task, another scene's task and unknown IDs are
	// all not_found; nothing is created for them.
	other := f.params
	other.Source.Key, other.RequesterRef = "other-requester", "human:b"
	foreign := createTaskWith(t, f, other)
	ws, _ := util.ParseUUID(upstream.Scope.WorkspaceID)
	agent, _ := util.ParseUUID(upstream.Scope.AgentID)
	row, err := scene.Resolve(ctx, db.New(f.pool), scene.Owner{WorkspaceID: ws, AgentID: agent}, scene.DingTalkConversation(upstream.Scope.TenantOrgID, scene.KindDM, "cid-"+uuid.NewString()), scene.Observation{KindStated: true})
	if err != nil {
		t.Fatal(err)
	}
	dm := f.params
	dm.Source.Key, dm.Scope.Scene = "dm-task", scene.RefOf(row)
	dmTask := createTaskWith(t, f, dm)
	for name, related := range map[string]string{"requester": foreign.ID, "scene": dmTask.ID, "unknown": uuid.NewString()} {
		bad := f.params
		bad.Source.Key, bad.BuildsOn = "bad-"+name, []string{related}
		if _, err := f.store.Create(ctx, bad); !errors.Is(err, ErrNotFound) {
			t.Fatalf("builds_on %s accepted: %v", name, err)
		}
	}
	for name, ids := range map[string][]string{"too many": {uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()}, "duplicate": {upstream.ID, upstream.ID}, "not uuid": {"t1"}} {
		bad := f.params
		bad.Source.Key, bad.BuildsOn = "invalid-"+name, ids
		if _, err := f.store.Create(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("builds_on %s accepted: %v", name, err)
		}
	}
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM employee_task WHERE source_key LIKE 'bad-%' OR source_key LIKE 'invalid-%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("refused builds_on left tasks: %d %v", n, err)
	}
}

func createTaskWith(t *testing.T, f fixture, p CreateParams) Task {
	t.Helper()
	task, err := f.store.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestUpstreamReportUsesLatestSuccessfulRun(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := createTask(t, f)
	report, err := f.store.UpstreamReport(ctx, task.Scope, task.ID)
	if err != nil || report.RunID != "" || report.State != StateReady || report.Goal != f.params.Definition.Goal {
		t.Fatalf("no run yet: %+v %v", report, err)
	}
	task, first := runToTerminal(t, f, task, "first", StateSucceeded)
	task, _, err = f.store.Resume(ctx, task.Scope, task.ID, ResumeParams{Source: Source{"human", "more"}, ActorRef: "human:a", Body: "add more", ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	_, second := runToTerminal(t, f, task, "second", StateSucceeded)
	report, err = f.store.UpstreamReport(ctx, task.Scope, task.ID)
	if err != nil || report.RunID != second.ID || report.Result != "second succeeded" || report.RunID == first.ID {
		t.Fatalf("latest report: %+v %v", report, err)
	}
	failed := f.params
	failed.Source.Key = "failed-upstream"
	onlyFailed, _ := runToTerminal(t, f, createTaskWith(t, f, failed), "broken", StateFailed)
	report, err = f.store.UpstreamReport(ctx, onlyFailed.Scope, onlyFailed.ID)
	if err != nil || report.RunID != "" || report.Result != "" || report.State != StateFailed {
		t.Fatalf("a failed Run is not an upstream result: %+v %v", report, err)
	}
	other := task.Scope
	other.TenantOrgID = "org-b"
	if _, err = f.store.UpstreamReport(ctx, other, task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("report crossed scope: %v", err)
	}
}

func blockParams(upstream Task, key string) BlockParams {
	return BlockParams{Source: Source{"task_dependency", key}, UpstreamTaskID: upstream.ID, AuthorityRef: authorityOf(upstream)}
}

func releaseInTx(t *testing.T, f fixture, upstream Task, key string) (UpstreamRelease, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	release, err := ReleaseUpstreamWaitTx(ctx, tx, upstream.Scope, upstream.ID, Source{"upstream_terminal", key})
	if err != nil {
		return release, err
	}
	return release, tx.Commit(ctx)
}

func TestBlockedByWaitsForTheUpstreamTerminalFact(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	upstream := createTask(t, f)
	run, err := f.store.StartRun(ctx, upstream.Scope, upstream.ID, StartRunParams{Source: Source{"host", "count"}, QueueTaskID: uuid.NewString(), ExpectedVersion: upstream.Version})
	if err != nil {
		t.Fatal(err)
	}
	down := createGoal(t, f, "retro")
	down, _ = runToTerminal(t, f, down, "draft", StateSucceeded)
	p := blockParams(upstream, "retro/blocked-by-count")
	down, link, wait, err := f.store.BlockOnTask(ctx, down.Scope, down.ID, p)
	if err != nil || down.State != StateWaiting || link.Relation != RelationBlockedBy || link.RelatedTaskID != upstream.ID || wait.Kind != WaitUpstreamTask || wait.OpenedSeq != link.EntrySeq {
		t.Fatalf("block: %+v %+v %+v %v", down, link, wait, err)
	}
	again, replayLink, _, err := f.store.BlockOnTask(ctx, down.Scope, down.ID, p)
	if err != nil || again.Version != down.Version || replayLink.EntrySeq != link.EntrySeq {
		t.Fatalf("replay: %+v %v", again, err)
	}
	_, _, err = f.store.CompleteGoal(ctx, down.Scope, down.ID, completion(down, "too-early"))
	requireRefusal(t, err, CodeNotReady, ReasonOpenMandatoryWait)
	_, err = releaseInTx(t, f, upstream, "early")
	requireRefusal(t, err, CodeNotReady, ReasonUpstreamNotTerminal)

	if _, _, err = f.store.RecordResult(ctx, upstream.Scope, upstream.ID, ResultParams{Source: Source{"queue_terminal", run.QueueTaskID}, RunID: run.ID, State: StateSucceeded, Result: "42 bugs"}); err != nil {
		t.Fatal(err)
	}
	release, err := releaseInTx(t, f, upstream, run.QueueTaskID)
	if err != nil || release.UpstreamState != StateSucceeded || len(release.Dependents) != 1 || release.Dependents[0] != (ReleasedDependent{down.ID, ReleaseSatisfied}) {
		t.Fatalf("release: %+v %v", release, err)
	}
	down, err = f.store.Get(ctx, down.Scope, down.ID)
	if err != nil || down.State != StateReady {
		t.Fatalf("dependent not ready: %+v %v", down, err)
	}
	replayed, err := releaseInTx(t, f, upstream, run.QueueTaskID)
	if err != nil || replayed.Dependents[0].Outcome != ReleaseSkipped {
		t.Fatalf("release replay: %+v %v", replayed, err)
	}
	if _, _, err = f.store.CompleteGoal(ctx, down.Scope, down.ID, completion(down, "after-upstream")); err != nil {
		t.Fatal(err)
	}

	// A completed upstream needs no wait; one that failed would block forever.
	late := createGoal(t, f, "late")
	_, _, _, err = f.store.BlockOnTask(ctx, late.Scope, late.ID, blockParams(upstream, "late"))
	requireRefusal(t, err, CodeConflict, ReasonUpstreamCompleted)
	failedParams := f.params
	failedParams.Source.Key = "failed-upstream"
	failed, _ := runToTerminal(t, f, createTaskWith(t, f, failedParams), "broken", StateFailed)
	_, _, _, err = f.store.BlockOnTask(ctx, late.Scope, late.ID, blockParams(failed, "on-failed"))
	requireRefusal(t, err, CodeConflict, ReasonUpstreamNotLanded)
	other := f.params
	other.Source.Key, other.RequesterRef = "other-requester", "human:b"
	foreign := createTaskWith(t, f, other)
	if _, _, _, err = f.store.BlockOnTask(ctx, late.Scope, late.ID, blockParams(foreign, "foreign")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("blocked on another requester's task: %v", err)
	}
	legacy := createTask(t, f)
	if _, _, _, err = f.store.BlockOnTask(ctx, legacy.Scope, legacy.ID, blockParams(foreign, "v1")); err == nil {
		t.Fatal("a v1 task accepted a dependency wait")
	}
}

func TestBlockedByFailedOrStoppedUpstreamHoldsDependents(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	upstream := createGoal(t, f, "upstream-goal")
	held := createGoal(t, f, "held")
	held, _, _, err := f.store.BlockOnTask(ctx, held.Scope, held.ID, blockParams(upstream, "held"))
	if err != nil {
		t.Fatal(err)
	}
	stopped := createGoal(t, f, "stopped-dependent")
	stopped, _, _, err = f.store.BlockOnTask(ctx, stopped.Scope, stopped.ID, blockParams(upstream, "stopped"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.store.Stop(ctx, stopped.Scope, stopped.ID, StopParams{Source: Source{"human", "stop-dependent"}, ActorRef: "human:a", Body: "stop", ExpectedVersion: stopped.Version}); err != nil {
		t.Fatal(err)
	}
	// Cycles are refused: the upstream may not wait for its own dependent.
	_, _, _, err = f.store.BlockOnTask(ctx, upstream.Scope, upstream.ID, blockParams(held, "cycle"))
	requireRefusal(t, err, CodeConflict, ReasonDependencyCycle)
	if _, _, err = f.store.Stop(ctx, upstream.Scope, upstream.ID, StopParams{Source: Source{"human", "stop-upstream"}, ActorRef: "human:a", Body: "stop", ExpectedVersion: upstream.Version}); err != nil {
		t.Fatal(err)
	}
	release, err := releaseInTx(t, f, upstream, "stopped")
	if err != nil || release.UpstreamState != StateCancelled {
		t.Fatalf("release: %+v %v", release, err)
	}
	got := map[string]ReleaseOutcome{}
	for _, d := range release.Dependents {
		got[d.TaskID] = d.Outcome
	}
	if got[held.ID] != ReleaseHeld || got[stopped.ID] != ReleaseSkipped {
		t.Fatalf("outcomes: %+v", got)
	}
	held, err = f.store.Get(ctx, held.Scope, held.ID)
	if err != nil || held.State != StateWaiting {
		t.Fatalf("a dependent of work that did not land was released: %+v %v", held, err)
	}
}

// An upstream that ends while a dependency is being recorded is never lost:
// either the block sees the ended upstream, or the release finds the wait.
func TestBlockedByRacingUpstreamTerminalNeverStrandsTheWait(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	for i := range 8 {
		upstream := createGoal(t, f, uuid.NewString())
		upstream, _ = runToTerminal(t, f, upstream, "work-"+upstream.ID, StateSucceeded)
		down := createGoal(t, f, uuid.NewString())
		var wg sync.WaitGroup
		var blockErr, completeErr error
		wg.Go(func() {
			_, _, _, blockErr = f.store.BlockOnTask(ctx, down.Scope, down.ID, blockParams(upstream, "race"))
		})
		wg.Go(func() {
			tx, err := f.pool.Begin(ctx)
			if err != nil {
				completeErr = err
				return
			}
			defer tx.Rollback(ctx)
			if _, _, err = CompleteGoalTx(ctx, tx, upstream.Scope, upstream.ID, completion(upstream, "done")); err != nil {
				completeErr = err
				return
			}
			if _, err = ReleaseUpstreamWaitTx(ctx, tx, upstream.Scope, upstream.ID, Source{"upstream_terminal", upstream.ID}); err != nil {
				completeErr = err
				return
			}
			completeErr = tx.Commit(ctx)
		})
		wg.Wait()
		if completeErr != nil {
			t.Fatalf("iteration %d complete: %v", i, completeErr)
		}
		got, err := f.store.Get(ctx, down.Scope, down.ID)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case blockErr == nil && got.State != StateReady:
			t.Fatalf("iteration %d: wait stranded after the upstream completed: %+v", i, got)
		case blockErr != nil && ErrorCodeOf(blockErr) != CodeConflict:
			t.Fatalf("iteration %d: block: %v", i, blockErr)
		}
	}
}
