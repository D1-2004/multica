package employeetask

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func createGoal(t *testing.T, f fixture, key string) Task {
	t.Helper()
	p := f.params
	p.Source.Key = key
	p.Lifecycle = LifecycleV2
	p.CompletionMode = CompletionExplicitGoal
	task, err := f.store.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if task.Lifecycle() != LifecycleV2 || task.CompletionMode != CompletionExplicitGoal || task.State != StateReady {
		t.Fatalf("goal lifecycle not frozen at creation: %+v", task)
	}
	return task
}

func authorityOf(task Task) string { return fmt.Sprintf("employee_task_entry:%s/1", task.ID) }

func completion(task Task, key string) CompleteGoalParams {
	return CompleteGoalParams{Source: Source{"wake", key}, GoalRevision: task.GoalRevision, InputSeq: task.LastEntrySeq, AuthorityRef: authorityOf(task), Summary: "total 31", ExpectedVersion: task.Version}
}

func collectionWait(task Task, ref string) WaitParams {
	return WaitParams{Source: Source{"collection", ref + "/open"}, Kind: WaitCollection, RefID: ref, Mandatory: true, AuthorityRef: authorityOf(task), Body: "waiting for three answers"}
}

func satisfy(task Task, ref string) ReadyParams {
	return ReadyParams{Source: Source{"collection", ref + "/ready"}, Kind: WaitCollection, RefID: ref, Outcome: WaitSatisfied, EvidenceRef: "collection:" + ref + "/revision/3", AuthorityRef: authorityOf(task)}
}

func requireRefusal(t *testing.T, err error, code ErrorCode, reason string) {
	t.Helper()
	var lifecycle *LifecycleError
	if !errors.As(err, &lifecycle) || lifecycle.Code != code || (reason != "" && lifecycle.Reason != reason) {
		t.Fatalf("want %s/%s refusal, got %v", code, reason, err)
	}
	if ErrorCodeOf(err) != code {
		t.Fatalf("ErrorCodeOf(%v)=%s want %s", err, ErrorCodeOf(err), code)
	}
}

func runToTerminal(t *testing.T, f fixture, task Task, key string, state State) (Task, Run) {
	t.Helper()
	ctx := context.Background()
	current, err := f.store.Get(ctx, task.Scope, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"host", key}, QueueTaskID: uuid.NewString(), ExpectedVersion: current.Version})
	if err != nil {
		t.Fatalf("start %s: %v", key, err)
	}
	after, finished, err := f.store.RecordResult(ctx, task.Scope, task.ID, ResultParams{Source: Source{"queue_terminal", run.QueueTaskID}, RunID: run.ID, State: state, Result: key + " " + string(state)})
	if err != nil || finished.State != state {
		t.Fatalf("terminal %s: %+v %v", key, finished, err)
	}
	return after, finished
}

func entryCount(t *testing.T, pool *pgxpool.Pool, taskID, kind string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM employee_task_entry WHERE task_id=$1::uuid AND kind=$2`, taskID, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// waitForRowLock observes a real PostgreSQL lock wait on any row.
func waitForRowLock(t *testing.T, pool *pgxpool.Pool, pid uint32, finished <-chan struct{}) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(context.Background(), `SELECT cardinality(pg_blocking_pids($1)) > 0`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-finished:
			t.Fatal("operation did not wait for the completion lock")
		case <-deadline.C:
			t.Fatal("operation did not reach a PostgreSQL lock wait")
		case <-tick.C:
		}
	}
}

func TestExplicitGoalRunSuccessDoesNotFinishWaitingTask(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := createGoal(t, f, "collect-three")
	task, wait, err := f.store.WaitTask(ctx, task.Scope, task.ID, collectionWait(task, "c1"))
	if err != nil || task.State != StateWaiting || wait.State != WaitOpen || !wait.Mandatory || wait.Kind != WaitCollection || wait.GoalRevision != 1 {
		t.Fatalf("open wait: %+v %+v %v", task, wait, err)
	}
	// The asking Run may execute while the goal waits for the answers.
	run, err := f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"host", "ask"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if current, _ := f.store.Get(ctx, task.Scope, task.ID); current.State != StateRunning || current.ActiveRunID != run.ID {
		t.Fatalf("waiting goal did not run: %+v", current)
	}
	result := ResultParams{Source: Source{"queue_terminal", run.QueueTaskID}, RunID: run.ID, State: StateSucceeded, Result: "asked three people"}
	after, finished, err := f.store.RecordResult(ctx, task.Scope, task.ID, result)
	if err != nil || finished.State != StateSucceeded {
		t.Fatalf("run result: %+v %v", finished, err)
	}
	if after.State != StateWaiting || after.ActiveRunID != "" {
		t.Fatalf("a successful Run finished a goal with an open mandatory wait: %+v", after)
	}
	replay, _, err := f.store.RecordResult(ctx, task.Scope, task.ID, result)
	if err != nil || replay.Version != after.Version || replay.State != StateWaiting {
		t.Fatalf("terminal replay: %+v %v", replay, err)
	}
	if n := entryCount(t, f.pool, task.ID, "goal_completed"); n != 0 {
		t.Fatalf("Run success emitted %d completion entries", n)
	}

	// Without waits a successful Run still leaves the goal for an explicit decision.
	other := createGoal(t, f, "single-step-goal")
	other, _ = runToTerminal(t, f, other, "only-run", StateSucceeded)
	if other.State != StateReady {
		t.Fatalf("v2 Run success completed the goal: %+v", other)
	}
}

func TestSingleRunLegacyResultStillFinishesTask(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := createTask(t, f)
	if task.Lifecycle() != LifecycleV1 || task.LifecycleVersion != LifecycleV1 || task.CompletionMode != CompletionSingleRun || task.AutonomousRounds != 0 {
		t.Fatalf("default lifecycle: %+v", task)
	}
	var lifecycleKeys bool
	if err := f.pool.QueryRow(ctx, `SELECT create_payload ? 'lifecycle_version' OR create_payload ? 'completion_mode' FROM employee_task WHERE id=$1::uuid`, task.ID).Scan(&lifecycleKeys); err != nil || lifecycleKeys {
		t.Fatalf("v1 create payload changed bytes for old binaries: %v %v", lifecycleKeys, err)
	}
	explicit := f.params
	explicit.Lifecycle, explicit.CompletionMode = LifecycleV1, CompletionSingleRun
	again, err := f.store.Create(ctx, explicit)
	if err != nil || again.ID != task.ID {
		t.Fatalf("explicit v1 replay did not match the default payload: %+v %v", again, err)
	}
	done, _ := runToTerminal(t, f, task, "legacy", StateSucceeded)
	if done.State != StateSucceeded {
		t.Fatalf("v1 Run success must finish the task: %+v", done)
	}
	f.params.Source.Key = "legacy-failure"
	failed, _ := runToTerminal(t, f, createTask(t, f), "legacy-failure", StateFailed)
	if failed.State != StateFailed {
		t.Fatalf("v1 Run failure must fail the task: %+v", failed)
	}
	if _, _, err = f.store.WaitTask(ctx, failed.Scope, failed.ID, collectionWait(failed, "c1")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("v1 task accepted a wait: %v", err)
	}
	if _, _, err = f.store.CompleteGoal(ctx, done.Scope, done.ID, completion(done, "v1-complete")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("v1 task accepted explicit completion: %v", err)
	}
	// A row written by an old binary has no lifecycle columns and reads as v1.
	var legacyID string
	if err = f.pool.QueryRow(ctx, `INSERT INTO employee_task(workspace_id,agent_id,tenant_org_id,scope_kind,scene_id,owner_loop,dispatch_mode,requester_ref,definition,source_namespace,source_key,create_payload)
 VALUES($1::uuid,$2::uuid,$3,'scene',$4::uuid,'employee','direct','human:a','{"goal":"old binary"}','old','binary','{}') RETURNING id::text`, task.Scope.WorkspaceID, task.Scope.AgentID, task.Scope.TenantOrgID, task.Scope.Scene.SceneID).Scan(&legacyID); err != nil {
		t.Fatal(err)
	}
	legacy, err := f.store.Get(ctx, task.Scope, legacyID)
	if err != nil || legacy.Lifecycle() != LifecycleV1 || legacy.CompletionMode != CompletionSingleRun {
		t.Fatalf("old-binary row: %+v %v", legacy, err)
	}
	var pgErr *pgconn.PgError
	if _, err = f.pool.Exec(ctx, `UPDATE employee_task SET state='waiting' WHERE id=$1::uuid`, legacyID); !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("database accepted a waiting v1 task: %v", err)
	}
	// A snapshot frozen before lifecycle v2 decodes as v1.
	if (Task{}).Lifecycle() != LifecycleV1 {
		t.Fatal("zero lifecycle is not v1")
	}
}

func TestGoalCreationValidatesLifecycleContract(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	for name, mutate := range map[string]func(*CreateParams){
		"v2 single run":    func(p *CreateParams) { p.Lifecycle, p.CompletionMode = LifecycleV2, CompletionSingleRun },
		"v2 missing mode":  func(p *CreateParams) { p.Lifecycle = LifecycleV2 },
		"v1 explicit goal": func(p *CreateParams) { p.CompletionMode = CompletionExplicitGoal },
		"unknown version":  func(p *CreateParams) { p.Lifecycle, p.CompletionMode = 3, CompletionExplicitGoal },
		"v2 issue dispatch": func(p *CreateParams) {
			p.Lifecycle, p.CompletionMode, p.DispatchMode = LifecycleV2, CompletionExplicitGoal, DispatchIssue
		},
		"v2 coordinator": func(p *CreateParams) {
			p.Lifecycle, p.CompletionMode, p.OwnerLoop = LifecycleV2, CompletionExplicitGoal, LoopCoordinator
		},
	} {
		p := f.params
		p.Source.Key = name
		mutate(&p)
		if _, err := f.store.Create(ctx, p); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	goal := createGoal(t, f, "goal")
	var pgErr *pgconn.PgError
	if _, err := f.pool.Exec(ctx, `UPDATE employee_task SET state='failed' WHERE id=$1::uuid`, goal.ID); !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("database accepted a failed v2 goal: %v", err)
	}
	v1 := f.params
	v1.Source.Key = "goal"
	if _, err := f.store.Create(ctx, v1); !errors.Is(err, ErrConflict) {
		t.Fatalf("same source with a different lifecycle replayed: %v", err)
	}
}

func TestCompleteGoalRejectsUnsatisfiedWaitStaleVersionAndStopped(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := createGoal(t, f, "collect")
	task, _ = runToTerminal(t, f, task, "ask", StateSucceeded)
	task, _, err := f.store.WaitTask(ctx, task.Scope, task.ID, collectionWait(task, "c1"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.store.CompleteGoal(ctx, task.Scope, task.ID, completion(task, "early"))
	requireRefusal(t, err, CodeNotReady, ReasonOpenMandatoryWait)
	task, wait, err := f.store.ReadyTask(ctx, task.Scope, task.ID, satisfy(task, "c1"))
	if err != nil || task.State != StateReady || wait.State != WaitSatisfied || wait.Revision != 2 || wait.EvidenceRef == "" {
		t.Fatalf("ready: %+v %+v %v", task, wait, err)
	}

	// A human input commits on another connection after the wake froze its read.
	frozen := task
	conn, err := f.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task, _, err = NewStore(conn).AppendInput(ctx, task.Scope, task.ID, InputParams{Source: Source{"dispatch", "late-answer"}, ActorRef: "human:a", Body: "also include Ding", ExpectedVersion: task.Version})
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.store.CompleteGoal(ctx, task.Scope, task.ID, completion(frozen, "stale-read"))
	requireRefusal(t, err, CodeConflict, ReasonStaleVersion)

	// The current version but an older input boundary is still a stale summary.
	boundary := completion(task, "old-boundary")
	boundary.InputSeq = frozen.LastEntrySeq
	_, _, err = f.store.CompleteGoal(ctx, task.Scope, task.ID, boundary)
	requireRefusal(t, err, CodeConflict, ReasonInputAfterBoundary)

	// A summary of the old goal revision cannot complete the corrected goal.
	amended := Definition{Goal: "Collect four numbers"}
	task, _, err = f.store.AppendInput(ctx, task.Scope, task.ID, InputParams{Source: Source{"dispatch", "amend"}, ActorRef: "human:a", Body: "ask four people", Correction: &amended, ExpectedVersion: task.Version})
	if err != nil || task.GoalRevision != 2 || task.State != StateReady {
		t.Fatalf("amend: %+v %v", task, err)
	}
	oldRevision := completion(task, "old-revision")
	oldRevision.GoalRevision = 1
	_, _, err = f.store.CompleteGoal(ctx, task.Scope, task.ID, oldRevision)
	requireRefusal(t, err, CodeConflict, ReasonStaleGoalRevision)

	// An active writer, then an unconfirmed exit, keep the goal open.
	run, err := f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"host", "summarize"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	task, _ = f.store.Get(ctx, task.Scope, task.ID)
	evidenced := completion(task, "while-running")
	evidenced.EvidenceRef = "collection:c1/summary"
	_, _, err = f.store.CompleteGoal(ctx, task.Scope, task.ID, evidenced)
	requireRefusal(t, err, CodeNotReady, ReasonActiveWriter)
	if !errors.Is(err, ErrActiveRun) {
		t.Fatalf("active writer refusal lost ErrActiveRun: %v", err)
	}
	task, _, err = f.store.RecordResult(ctx, task.Scope, task.ID, ResultParams{Source: Source{"queue_terminal", run.QueueTaskID}, RunID: run.ID, State: StateCancelled, Result: "task cancelled"})
	if err != nil || task.State != StateReady {
		t.Fatalf("cancelled Run: %+v %v", task, err)
	}
	evidenced = completion(task, "unconfirmed-exit")
	evidenced.EvidenceRef = "collection:c1/summary"
	_, _, err = f.store.CompleteGoal(ctx, task.Scope, task.ID, evidenced)
	requireRefusal(t, err, CodeNotReady, ReasonPendingWriter)
	task, _, err = f.store.FenceRunWriter(ctx, task.Scope, task.ID, FenceWriterParams{Source: Source{"fence", run.ID}, RunID: run.ID, Evidence: FenceProcessStopped})
	if err != nil {
		t.Fatal(err)
	}

	// The completion holds the aggregate lock; a concurrent input waits for it
	// and then loses its CAS instead of being silently absorbed.
	evidenced = completion(task, "final")
	evidenced.EvidenceRef = "collection:c1/summary"
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	done, entry, err := CompleteGoalTx(ctx, tx, task.Scope, task.ID, evidenced)
	if err != nil || done.State != StateSucceeded || entry.Kind != "goal_completed" {
		t.Fatalf("complete: %+v %+v %v", done, entry, err)
	}
	writer, err := f.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pid := writer.Conn().PgConn().PID()
	finished := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		defer close(finished)
		defer writer.Release()
		_, _, err := NewStore(writer).AppendInput(ctx, task.Scope, task.ID, InputParams{Source: Source{"dispatch", "racing"}, ActorRef: "human:a", Body: "one more thing", ExpectedVersion: task.Version})
		result <- err
	}()
	waitForRowLock(t, f.pool, pid, finished)
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-result; !errors.Is(err, ErrConflict) {
		t.Fatalf("racing input was absorbed by a completed summary: %v", err)
	}

	// A human stop is reported as stopped, not as a stale version.
	stopped := createGoal(t, f, "stopped")
	stopped, _, err = f.store.WaitTask(ctx, stopped.Scope, stopped.ID, collectionWait(stopped, "c2"))
	if err != nil {
		t.Fatal(err)
	}
	beforeStop := stopped
	stopped, _, err = f.store.Stop(ctx, stopped.Scope, stopped.ID, StopParams{Source: Source{"human", "stop"}, ActorRef: "human:a", Body: "stop collecting", ExpectedVersion: stopped.Version})
	if err != nil || stopped.State != StateCancelled {
		t.Fatalf("stop: %+v %v", stopped, err)
	}
	_, _, err = f.store.CompleteGoal(ctx, stopped.Scope, stopped.ID, completion(beforeStop, "after-stop"))
	requireRefusal(t, err, CodeStopped, ReasonStopped)
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("stopped refusal lost ErrStopped: %v", err)
	}
	if n := entryCount(t, f.pool, stopped.ID, "goal_completed"); n != 0 {
		t.Fatalf("stopped goal completed %d times", n)
	}
}

func TestCompleteGoalZeroWorkReturnsNotReady(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := createGoal(t, f, "zero-work")
	_, _, err := f.store.CompleteGoal(ctx, task.Scope, task.ID, completion(task, "nothing-done"))
	requireRefusal(t, err, CodeNotReady, ReasonNoExecutionEvidence)
	got, _ := f.store.Get(ctx, task.Scope, task.ID)
	if got.State != StateReady || got.Version != task.Version || entryCount(t, f.pool, task.ID, "goal_completed") != 0 {
		t.Fatalf("zero-work completion changed the goal: %+v", got)
	}
	// A failed Run is not execution evidence either.
	task, _ = runToTerminal(t, f, task, "failed-run", StateFailed)
	task, _, err = f.store.FenceRunWriter(ctx, task.Scope, task.ID, FenceWriterParams{Source: Source{"fence", "failed-run"}, RunID: mustLatestRun(t, f, task).ID, Evidence: FenceProcessStopped})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.store.CompleteGoal(ctx, task.Scope, task.ID, completion(task, "after-failure"))
	requireRefusal(t, err, CodeNotReady, ReasonNoExecutionEvidence)

	// A Run that succeeded for an older goal revision does not prove the corrected goal.
	other := createGoal(t, f, "old-evidence")
	other, _ = runToTerminal(t, f, other, "old-run", StateSucceeded)
	amended := Definition{Goal: "A different deliverable"}
	other, _, err = f.store.AppendInput(ctx, other.Scope, other.ID, InputParams{Source: Source{"dispatch", "amend"}, ActorRef: "human:a", Body: "change it", Correction: &amended, ExpectedVersion: other.Version})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.store.CompleteGoal(ctx, other.Scope, other.ID, completion(other, "old-evidence"))
	requireRefusal(t, err, CodeNotReady, ReasonNoExecutionEvidence)
	explicit := completion(other, "explicit-evidence")
	explicit.EvidenceRef = "employee_outbox:summary-1"
	done, entry, err := f.store.CompleteGoal(ctx, other.Scope, other.ID, explicit)
	if err != nil || done.State != StateSucceeded || entry.GoalRevision != 2 {
		t.Fatalf("explicit evidence: %+v %+v %v", done, entry, err)
	}
}

func mustLatestRun(t *testing.T, f fixture, task Task) Run {
	t.Helper()
	run, err := f.store.LatestRun(context.Background(), task.Scope, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestCompleteGoalReplayIsIdempotentAndSingle(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := createGoal(t, f, "replay")
	task, run := runToTerminal(t, f, task, "work", StateSucceeded)
	p := completion(task, "summary")
	done, entry, err := f.store.CompleteGoal(ctx, task.Scope, task.ID, p)
	if err != nil || done.State != StateSucceeded || entry.Kind != "goal_completed" || entry.RunID != run.ID || entry.Body != p.Summary {
		t.Fatalf("complete: %+v %+v %v", done, entry, err)
	}
	again, replayed, err := f.store.CompleteGoal(ctx, task.Scope, task.ID, p)
	if err != nil || replayed.Seq != entry.Seq || again.Version != done.Version {
		t.Fatalf("replay: %+v %+v %v", again, replayed, err)
	}
	changed := p
	changed.Summary = "total 32"
	if _, _, err = f.store.CompleteGoal(ctx, task.Scope, task.ID, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same source with different content replayed: %v", err)
	}
	second := p
	second.Source.Key = "second-summary"
	second.ExpectedVersion = done.Version
	_, _, err = f.store.CompleteGoal(ctx, task.Scope, task.ID, second)
	requireRefusal(t, err, CodeConflict, ReasonAlreadyCompleted)
	if n := entryCount(t, f.pool, task.ID, "goal_completed"); n != 1 {
		t.Fatalf("completion entries=%d", n)
	}

	// Two deciding wakes race on separate connections; exactly one completes.
	racing := createGoal(t, f, "racing")
	racing, _ = runToTerminal(t, f, racing, "racing-work", StateSucceeded)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, key := range []string{"wake-a", "wake-b"} {
		wg.Go(func() {
			_, _, err := f.store.CompleteGoal(ctx, racing.Scope, racing.ID, completion(racing, key))
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if success != 1 || entryCount(t, f.pool, racing.ID, "goal_completed") != 1 {
		t.Fatalf("racing completions: success=%d", success)
	}
	// The database also refuses a second completion of one goal revision.
	var pgErr *pgconn.PgError
	_, err = f.pool.Exec(ctx, `INSERT INTO employee_task_entry(workspace_id,agent_id,tenant_org_id,task_id,seq,kind,source_namespace,source_key,goal_revision,payload)
 SELECT workspace_id,agent_id,tenant_org_id,id,last_entry_seq+1,'goal_completed','forged','second',goal_revision,'{}' FROM employee_task WHERE id=$1::uuid`, racing.ID)
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("second completion row accepted: %v", err)
	}
}

func TestLifecycleTransitionTable(t *testing.T) {
	type edge struct{ from, to State }
	// Reference contract (01-foundation §2 plus the accepted v1 behavior).
	spec := map[LifecycleVersion]map[edge]bool{
		LifecycleV1: {
			{StateReady, StateRunning}: true, {StateReady, StateCancelled}: true,
			{StateRunning, StateReady}: true, {StateRunning, StateSucceeded}: true, {StateRunning, StateFailed}: true, {StateRunning, StateCancelled}: true,
			{StateSucceeded, StateReady}: true, {StateSucceeded, StateRunning}: true, {StateSucceeded, StateCancelled}: true,
			{StateFailed, StateReady}: true, {StateFailed, StateRunning}: true, {StateFailed, StateCancelled}: true,
			{StateCancelled, StateReady}: true, {StateCancelled, StateRunning}: true,
		},
		LifecycleV2: {
			{StateReady, StateRunning}: true, {StateReady, StateWaiting}: true, {StateReady, StateSucceeded}: true, {StateReady, StateCancelled}: true,
			{StateWaiting, StateRunning}: true, {StateWaiting, StateReady}: true, {StateWaiting, StateCancelled}: true,
			{StateRunning, StateReady}: true, {StateRunning, StateWaiting}: true, {StateRunning, StateCancelled}: true,
			{StateSucceeded, StateReady}: true, {StateSucceeded, StateCancelled}: true,
		},
	}
	valid := map[LifecycleVersion]map[State]bool{
		LifecycleV1: {StateReady: true, StateRunning: true, StateSucceeded: true, StateFailed: true, StateCancelled: true},
		LifecycleV2: {StateReady: true, StateRunning: true, StateWaiting: true, StateSucceeded: true, StateCancelled: true},
	}
	states := append(AllStates(), State("unknown"))
	for _, version := range []LifecycleVersion{0, LifecycleV1, LifecycleV2, 3} {
		for _, from := range states {
			for _, to := range states {
				want := valid[version][from] && valid[version][to] && (from == to || spec[version][edge{from, to}])
				if got := TransitionAllowed(version, from, to); got != want {
					t.Errorf("v%d %s->%s allowed=%v want %v", version, from, to, got, want)
				}
				reachable := false
				for _, c := range allCauses {
					err := transition(version, from, to, c)
					if err == nil {
						reachable = true
						continue
					}
					var lifecycle *LifecycleError
					if !errors.As(err, &lifecycle) || lifecycle.Code != CodeConflict || lifecycle.Reason != ReasonIllegalTransition || lifecycle.From != from || lifecycle.To != to || !errors.Is(err, ErrConflict) {
						t.Errorf("v%d %s->%s via %s: untyped refusal %v", version, from, to, c, err)
					}
				}
				if reachable != want {
					t.Errorf("v%d %s->%s chokepoint reachable=%v want %v", version, from, to, reachable, want)
				}
			}
		}
	}
	// Invariants the contract depends on, independent of the spec rows above.
	for _, from := range states {
		if TransitionAllowed(LifecycleV1, from, StateWaiting) {
			t.Errorf("v1 reaches waiting from %s", from)
		}
		if TransitionAllowed(LifecycleV2, from, StateFailed) {
			t.Errorf("v2 goal reaches failed from %s", from)
		}
		if from != StateCancelled && TransitionAllowed(LifecycleV2, StateCancelled, from) {
			t.Errorf("v2 human stop lifted to %s", from)
		}
		for _, c := range allCauses {
			if from != StateSucceeded && c != causeCompleteGoal && transition(LifecycleV2, from, StateSucceeded, c) == nil {
				t.Errorf("v2 %s reaches succeeded via %s", from, c)
			}
			for _, to := range states {
				if from != to && (c == causeInput || c == causeBindIssue || c == causeFenceWriter || c == causeAutonomousWake) && (transition(LifecycleV1, from, to, c) == nil || transition(LifecycleV2, from, to, c) == nil) {
					t.Errorf("non-lifecycle cause %s moved %s->%s", c, from, to)
				}
			}
		}
	}
	if transition(LifecycleV2, StateRunning, StateSucceeded, causeRunResult) == nil {
		t.Error("a v2 Run terminal finished the goal")
	}
	if transition(LifecycleV2, StateSucceeded, StateReady, causeResume) == nil || transition(LifecycleV2, StateSucceeded, StateReady, causeSteer) == nil {
		t.Error("a v2 goal reopened without a new revision")
	}
}

func TestLifecycleChokepointRejectsIllegalWritesBeforePersisting(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := createGoal(t, f, "chokepoint")
	_, _, err := f.store.mutate(ctx, task.Scope, task.ID, Source{"test", "illegal"}, map[string]string{"probe": "1"}, 0, causeRunResult, func(_ pgx.Tx, t *Task) (Entry, error) {
		t.State = StateSucceeded
		return Entry{Kind: "input"}, nil
	})
	requireRefusal(t, err, CodeConflict, ReasonIllegalTransition)
	got, err := f.store.Get(ctx, task.Scope, task.ID)
	if err != nil || got.Version != task.Version || got.State != StateReady || got.LastEntrySeq != task.LastEntrySeq {
		t.Fatalf("illegal write persisted: %+v %v", got, err)
	}
	legacy := createTask(t, f)
	_, _, err = f.store.mutate(ctx, legacy.Scope, legacy.ID, Source{"test", "illegal"}, map[string]string{"probe": "1"}, 0, causeAmendment, func(_ pgx.Tx, t *Task) (Entry, error) {
		t.State = StateWaiting
		return Entry{Kind: "input"}, nil
	})
	requireRefusal(t, err, CodeConflict, ReasonIllegalTransition)
}

func TestWaitTaskReplayConflictAndProjection(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := createGoal(t, f, "waits")
	p := collectionWait(task, "c1")
	opened, wait, err := f.store.WaitTask(ctx, task.Scope, task.ID, p)
	if err != nil || opened.State != StateWaiting {
		t.Fatalf("open: %+v %v", opened, err)
	}
	again, replayed, err := f.store.WaitTask(ctx, task.Scope, task.ID, p)
	if err != nil || replayed.ID != wait.ID || again.Version != opened.Version {
		t.Fatalf("replay: %+v %+v %v", again, replayed, err)
	}
	changed := p
	changed.Mandatory = false
	if _, _, err = f.store.WaitTask(ctx, task.Scope, task.ID, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same source different wait: %v", err)
	}
	duplicate := p
	duplicate.Source.Key = "another-open"
	if _, _, err = f.store.WaitTask(ctx, task.Scope, task.ID, duplicate); !errors.Is(err, ErrConflict) {
		t.Fatalf("same domain wait opened twice: %v", err)
	}
	optional := WaitParams{Source: Source{"human", "h1/open"}, Kind: WaitHumanInput, RefID: "h1", AuthorityRef: authorityOf(task), Body: "optional detail"}
	withOptional, _, err := f.store.WaitTask(ctx, task.Scope, task.ID, optional)
	if err != nil || withOptional.State != StateWaiting {
		t.Fatalf("optional wait: %+v %v", withOptional, err)
	}
	if _, _, err = f.store.ReadyTask(ctx, task.Scope, task.ID, ReadyParams{Source: Source{"x", "y"}, Kind: WaitCollection, RefID: "missing", Outcome: WaitSatisfied, EvidenceRef: "e", AuthorityRef: authorityOf(task)}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown wait: %v", err)
	}
	bad := satisfy(task, "c1")
	bad.Outcome = WaitOpen
	if _, _, err = f.store.ReadyTask(ctx, task.Scope, task.ID, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("open is not a resolution: %v", err)
	}
	stale := satisfy(task, "c1")
	stale.ExpectedWaitRevision = 5
	if _, _, err = f.store.ReadyTask(ctx, task.Scope, task.ID, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale wait revision: %v", err)
	}
	ready, resolved, err := f.store.ReadyTask(ctx, task.Scope, task.ID, satisfy(task, "c1"))
	if err != nil || ready.State != StateReady || resolved.State != WaitSatisfied || resolved.ResolvedSeq <= resolved.OpenedSeq {
		t.Fatalf("an optional wait held the goal: %+v %+v %v", ready, resolved, err)
	}
	late := satisfy(task, "c1")
	late.Source.Key = "duplicate-ready"
	_, _, err = f.store.ReadyTask(ctx, task.Scope, task.ID, late)
	requireRefusal(t, err, CodeConflict, ReasonWaitResolved)
	cancelled := ReadyParams{Source: Source{"human", "h1/cancel"}, Kind: WaitHumanInput, RefID: "h1", Outcome: WaitCancelled, EvidenceRef: "requester withdrew", AuthorityRef: authorityOf(task)}
	if _, _, err = f.store.ReadyTask(ctx, task.Scope, task.ID, cancelled); err != nil {
		t.Fatal(err)
	}
	waits, err := f.store.Waits(ctx, task.Scope, task.ID)
	if err != nil || len(waits) != 2 || waits[0].State != WaitSatisfied || waits[1].State != WaitCancelled {
		t.Fatalf("waits: %+v %v", waits, err)
	}
	other := task.Scope
	other.TenantOrgID = "org-b"
	if _, _, err = f.store.WaitTask(ctx, other, task.ID, collectionWait(task, "c9")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("WaitTask crossed scope: %v", err)
	}
	if _, _, err = f.store.ReadyTask(ctx, other, task.ID, satisfy(task, "c1")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadyTask crossed scope: %v", err)
	}
	if _, _, err = f.store.CompleteGoal(ctx, other, task.ID, completion(task, "x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CompleteGoal crossed scope: %v", err)
	}
	if _, err = f.store.Waits(ctx, other, task.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Waits crossed scope: %v", err)
	}
	// A completed goal accepts no new dependency.
	current, _ := f.store.Get(ctx, task.Scope, task.ID)
	evidence := completion(current, "done")
	evidence.EvidenceRef = "collection:c1/summary"
	done, _, err := f.store.CompleteGoal(ctx, task.Scope, task.ID, evidence)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.store.WaitTask(ctx, done.Scope, done.ID, collectionWait(done, "c2"))
	requireRefusal(t, err, CodeConflict, ReasonAlreadyCompleted)
}

func TestAutonomousRoundsCountWakesAndResetOnHumanInput(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := createGoal(t, f, "governor")
	note := func(key string, want int64) Task {
		t.Helper()
		got, _, err := f.store.NoteAutonomousRound(ctx, task.Scope, task.ID, AutonomousRoundParams{Source: Source{"task_wake", key}, WakeKind: "collection.ready", AuthorityRef: authorityOf(task)})
		if err != nil || got.AutonomousRounds != want {
			t.Fatalf("round %s: %+v %v", key, got, err)
		}
		return got
	}
	note("w1", 1)
	note("w2", 2)
	note("w2", 2) // a redelivered wake counts once
	current := note("w3", 3)
	current, _, err := f.store.AppendInput(ctx, task.Scope, task.ID, InputParams{Source: Source{"dispatch", "human"}, ActorRef: "human:a", Body: "keep going", ExpectedVersion: current.Version})
	if err != nil || current.AutonomousRounds != 0 {
		t.Fatalf("human input did not reset: %+v %v", current, err)
	}
	note("w4", 1)
	current = note("w5", 2)
	current, _, err = f.store.Steer(ctx, task.Scope, task.ID, SteerParams{Source: Source{"steer", "human"}, ActorRef: "human:a", Body: "use the signed date"})
	if err != nil || current.AutonomousRounds != 0 {
		t.Fatalf("steer did not reset: %+v %v", current, err)
	}
	current, _, err = f.store.Stop(ctx, task.Scope, task.ID, StopParams{Source: Source{"human", "stop"}, ActorRef: "human:a", Body: "stop", ExpectedVersion: current.Version})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.store.NoteAutonomousRound(ctx, task.Scope, task.ID, AutonomousRoundParams{Source: Source{"task_wake", "after-stop"}, WakeKind: "collection.ready", AuthorityRef: authorityOf(task)})
	requireRefusal(t, err, CodeStopped, ReasonStopped)
}

func TestGoalStopIsFinalAndClosesWaits(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := createGoal(t, f, "stop")
	task, _, err := f.store.WaitTask(ctx, task.Scope, task.ID, collectionWait(task, "c1"))
	if err != nil {
		t.Fatal(err)
	}
	task, _, err = f.store.Stop(ctx, task.Scope, task.ID, StopParams{Source: Source{"human", "stop"}, ActorRef: "human:a", Body: "stop it", ExpectedVersion: task.Version})
	if err != nil || task.State != StateCancelled {
		t.Fatalf("stop: %+v %v", task, err)
	}
	waits, err := f.store.Waits(ctx, task.Scope, task.ID)
	if err != nil || len(waits) != 1 || waits[0].State != WaitCancelled {
		t.Fatalf("stop left open waits: %+v %v", waits, err)
	}
	corrected := Definition{Goal: "Restart please"}
	task, _, err = f.store.AppendInput(ctx, task.Scope, task.ID, InputParams{Source: Source{"dispatch", "correction"}, ActorRef: "human:a", Body: "restart", Correction: &corrected, ExpectedVersion: task.Version})
	if err != nil || task.State != StateCancelled {
		t.Fatalf("an input lifted the stop fence: %+v %v", task, err)
	}
	_, _, err = f.store.Steer(ctx, task.Scope, task.ID, SteerParams{Source: Source{"steer", "after-stop"}, ActorRef: "human:a", Body: "continue"})
	requireRefusal(t, err, CodeStopped, ReasonStopped)
	_, err = f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"host", "after-stop"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version})
	requireRefusal(t, err, CodeStopped, ReasonStopped)
	_, _, err = f.store.ReadyTask(ctx, task.Scope, task.ID, satisfy(task, "c1"))
	requireRefusal(t, err, CodeStopped, ReasonStopped)
	_, _, err = f.store.WaitTask(ctx, task.Scope, task.ID, collectionWait(task, "c2"))
	requireRefusal(t, err, CodeStopped, ReasonStopped)
	_, _, err = f.store.Resume(ctx, task.Scope, task.ID, ResumeParams{Source: Source{"human", "resume"}, ActorRef: "human:a", Body: "resume", ExpectedVersion: task.Version})
	requireRefusal(t, err, CodeStopped, ReasonStopped)
	got, _ := f.store.Get(ctx, task.Scope, task.ID)
	if got.State != StateCancelled || got.Version != task.Version {
		t.Fatalf("refused operations changed the stopped goal: %+v", got)
	}
}

func TestGoalFailedRunNeedsExplicitRetry(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := createGoal(t, f, "failure")
	task, run := runToTerminal(t, f, task, "first", StateFailed)
	if task.State != StateReady || run.State != StateFailed {
		t.Fatalf("a failed Run must leave the goal awaiting a decision: %+v %+v", task, run)
	}
	corrected := Definition{Goal: "Try a different source"}
	task, _, err := f.store.AppendInput(ctx, task.Scope, task.ID, InputParams{Source: Source{"dispatch", "correction"}, ActorRef: "human:a", Body: "use the other source", Correction: &corrected, ExpectedVersion: task.Version})
	if err != nil || task.State != StateReady {
		t.Fatalf("correction: %+v %v", task, err)
	}
	task, _, err = f.store.FenceRunWriter(ctx, task.Scope, task.ID, FenceWriterParams{Source: Source{"fence", run.ID}, RunID: run.ID, Evidence: FenceProcessStopped})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"host", "retry"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version})
	requireRefusal(t, err, CodeNotReady, ReasonRetryNotAuthorized)
	task, _, err = f.store.Steer(ctx, task.Scope, task.ID, SteerParams{Source: Source{"steer", "retry"}, ActorRef: "human:a", Body: "just retry"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"host", "steer-retry"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version})
	requireRefusal(t, err, CodeNotReady, ReasonRetryNotAuthorized)
	var runs int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM employee_task_run WHERE task_id=$1::uuid`, task.ID).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("implicit retry persisted: runs=%d %v", runs, err)
	}
}

func TestGoalSteerInterruptionStartsSuccessor(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := createGoal(t, f, "steer")
	task, run := runToTerminal(t, f, task, "first", StateCancelled)
	if task.State != StateReady {
		t.Fatalf("a cancelled Run stopped the goal: %+v", task)
	}
	task, _, err := f.store.FenceRunWriter(ctx, task.Scope, task.ID, FenceWriterParams{Source: Source{"fence", run.ID}, RunID: run.ID, Evidence: FenceClaimBarrier})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"host", "no-steer"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version})
	requireRefusal(t, err, CodeNotReady, ReasonRetryNotAuthorized)
	task, _, err = f.store.Steer(ctx, task.Scope, task.ID, SteerParams{Source: Source{"steer", "c1"}, ActorRef: "human:a", Body: "use the signed date", InterruptedRunID: run.ID})
	if err != nil || task.State != StateReady {
		t.Fatalf("steer: %+v %v", task, err)
	}
	next, err := f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"steer", "c1/run"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version})
	if err != nil || next.ID == run.ID {
		t.Fatalf("steer successor: %+v %v", next, err)
	}

	// A steer while the goal waits keeps it waiting; the successor may run.
	waiting := createGoal(t, f, "steer-waiting")
	waiting, _, err = f.store.WaitTask(ctx, waiting.Scope, waiting.ID, collectionWait(waiting, "c1"))
	if err != nil {
		t.Fatal(err)
	}
	waiting, _, err = f.store.Steer(ctx, waiting.Scope, waiting.ID, SteerParams{Source: Source{"steer", "w1"}, ActorRef: "human:a", Body: "ask Ding too"})
	if err != nil || waiting.State != StateWaiting {
		t.Fatalf("steer lost the open wait: %+v %v", waiting, err)
	}
}

func TestGoalWaitingIsActiveCandidateAndReopenNeedsAmendment(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := createGoal(t, f, "candidate")
	task, _, err := f.store.WaitTask(ctx, task.Scope, task.ID, collectionWait(task, "c1"))
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.store.ListCurrent(ctx, task.Scope, task.RequesterRef, time.Now().Add(time.Minute), 10)
	if err != nil || len(current) != 1 || current[0].State != StateWaiting || !current[0].State.Active() {
		t.Fatalf("waiting goal is not an active candidate: %+v %v", current, err)
	}
	for _, s := range []State{StateReady, StateSucceeded, StateFailed, StateCancelled} {
		if s.Active() {
			t.Fatalf("%s reported active", s)
		}
	}
	task, _, err = f.store.ReadyTask(ctx, task.Scope, task.ID, satisfy(task, "c1"))
	if err != nil {
		t.Fatal(err)
	}
	task, _ = runToTerminal(t, f, task, "summary", StateSucceeded)
	task, _, err = f.store.CompleteGoal(ctx, task.Scope, task.ID, completion(task, "done"))
	if err != nil || task.State != StateSucceeded {
		t.Fatalf("complete: %+v %v", task, err)
	}
	_, _, err = f.store.Resume(ctx, task.Scope, task.ID, ResumeParams{Source: Source{"human", "resume"}, ActorRef: "human:a", Body: "continue", ExpectedVersion: task.Version})
	requireRefusal(t, err, CodeConflict, ReasonReopenNeedsAmendment)
	_, _, err = f.store.Steer(ctx, task.Scope, task.ID, SteerParams{Source: Source{"steer", "after-done"}, ActorRef: "human:a", Body: "redo"})
	requireRefusal(t, err, CodeConflict, ReasonReopenNeedsAmendment)
	amended := Definition{Goal: "Also add the median"}
	task, _, err = f.store.AppendInput(ctx, task.Scope, task.ID, InputParams{Source: Source{"dispatch", "amend"}, ActorRef: "human:a", Body: "add the median", Correction: &amended, ExpectedVersion: task.Version})
	if err != nil || task.State != StateReady || task.GoalRevision != 2 {
		t.Fatalf("amendment did not reopen: %+v %v", task, err)
	}
	task, _ = runToTerminal(t, f, task, "median", StateSucceeded)
	task, entry, err := f.store.CompleteGoal(ctx, task.Scope, task.ID, completion(task, "done-again"))
	if err != nil || task.State != StateSucceeded || entry.GoalRevision != 2 || entryCount(t, f.pool, task.ID, "goal_completed") != 2 {
		t.Fatalf("revision 2 completion: %+v %+v %v", task, entry, err)
	}
}

// A Task-to-Task blocked_by wait is an ordinary mandatory wait fact: the
// dependent goal cannot complete until the upstream release is recorded.
func TestUpstreamTaskWaitBlocksCompletionUntilSatisfied(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	upstream := createTask(t, f)
	goal := createGoal(t, f, "downstream")
	goal, _ = runToTerminal(t, f, goal, "own-work", StateSucceeded)
	blocked := WaitParams{Source: Source{"task_dependency", upstream.ID + "/open"}, Kind: WaitUpstreamTask, RefID: upstream.ID, Mandatory: true, AuthorityRef: authorityOf(goal)}
	for name, ref := range map[string]string{"not a uuid": "upstream-1", "self": goal.ID, "uppercase": strings.ToUpper(upstream.ID)} {
		invalid := blocked
		invalid.Source.Key, invalid.RefID = name, ref
		if _, _, err := f.store.WaitTask(ctx, goal.Scope, goal.ID, invalid); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s upstream ref accepted: %v", name, err)
		}
	}
	goal, wait, err := f.store.WaitTask(ctx, goal.Scope, goal.ID, blocked)
	if err != nil || goal.State != StateWaiting || wait.Kind != WaitUpstreamTask || wait.RefID != upstream.ID {
		t.Fatalf("blocked_by wait: %+v %+v %v", goal, wait, err)
	}
	_, _, err = f.store.CompleteGoal(ctx, goal.Scope, goal.ID, completion(goal, "before-upstream"))
	requireRefusal(t, err, CodeNotReady, ReasonOpenMandatoryWait)
	goal, _, err = f.store.ReadyTask(ctx, goal.Scope, goal.ID, ReadyParams{Source: Source{"task_dependency", upstream.ID + "/released"}, Kind: WaitUpstreamTask, RefID: upstream.ID, Outcome: WaitSatisfied, EvidenceRef: "employee_task:" + upstream.ID + "/succeeded", AuthorityRef: authorityOf(goal)})
	if err != nil || goal.State != StateReady {
		t.Fatalf("release: %+v %v", goal, err)
	}
	done, _, err := f.store.CompleteGoal(ctx, goal.Scope, goal.ID, completion(goal, "after-upstream"))
	if err != nil || done.State != StateSucceeded {
		t.Fatalf("complete after release: %+v %v", done, err)
	}
	var pgErr *pgconn.PgError
	_, err = f.pool.Exec(ctx, `INSERT INTO employee_task_wait(workspace_id,agent_id,tenant_org_id,task_id,kind,ref_id,mandatory,goal_revision,opened_seq) VALUES($1::uuid,$2::uuid,$3,$4::uuid,'task','not-a-task',true,1,1)`, goal.Scope.WorkspaceID, goal.Scope.AgentID, goal.Scope.TenantOrgID, goal.ID)
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("database accepted a non-UUID upstream task ref: %v", err)
	}
}
