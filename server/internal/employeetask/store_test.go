package employeetask

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fixture struct {
	pool   *pgxpool.Pool
	store  *Store
	params CreateParams
}

// Fixtures apply real migrations in isolated schemas. Concurrent operations use
// separate PostgreSQL connections, never fake serialization.
func database(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL required for EmployeeTask PostgreSQL integration tests")
	}
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := "employee_task_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		if err != nil {
			t.Error(err)
		}
		_ = admin.Close(ctx)
	})
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, self, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(self), "..", "..", "migrations")
	for _, name := range []string{"9500_agent_scene.up.sql", "9501_agent_scene_id_idx.up.sql", "9502_agent_scene_locator_idx.up.sql", "9511_agent_scene_kind_source.up.sql"} {
		applyMigration(t, pool, filepath.Join(dir, name))
	}
	owned, err := filepath.Glob(filepath.Join(dir, "960*.up.sql"))
	if err != nil || len(owned) != 10 {
		t.Fatalf("owned migrations: %v %v", owned, err)
	}
	sort.Strings(owned)
	for _, path := range owned {
		applyMigration(t, pool, path)
	}
	applyMigration(t, pool, filepath.Join(dir, "9650_employee_task_resume.up.sql"))
	applyMigration(t, pool, filepath.Join(dir, "9760_employee_task_steer_entry.up.sql"))
	// Lifecycle v2 (P1) owns 9900-9909; 991x+ belong to other packages.
	lifecycle, err := filepath.Glob(filepath.Join(dir, "990*.up.sql"))
	if err != nil || len(lifecycle) != 5 {
		t.Fatalf("lifecycle v2 migrations: %v %v", lifecycle, err)
	}
	sort.Strings(lifecycle)
	for _, path := range lifecycle {
		applyMigration(t, pool, path)
	}
	ws, agent := uuid.NewString(), uuid.NewString()
	// The production workspace owns the parent-row lock shared with teardown.
	if _, err := pool.Exec(ctx, `CREATE TABLE workspace (id uuid NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workspace(id) VALUES($1::uuid)`, ws); err != nil {
		t.Fatal(err)
	}
	wsID, err := util.ParseUUID(ws)
	if err != nil {
		t.Fatal(err)
	}
	agentID, err := util.ParseUUID(agent)
	if err != nil {
		t.Fatal(err)
	}
	row, err := scene.Resolve(ctx, db.New(pool), scene.Owner{WorkspaceID: wsID, AgentID: agentID}, scene.DingTalkConversation("org-a", scene.KindGroup, "cid-"+uuid.NewString()), scene.Observation{KindStated: true})
	if err != nil {
		t.Fatal(err)
	}
	p := CreateParams{Scope: Scope{WorkspaceID: ws, AgentID: agent, TenantOrgID: "org-a", Kind: ScopeScene, Scene: scene.RefOf(row)}, OwnerLoop: LoopEmployee, DispatchMode: DispatchDirect, RequesterRef: "human:a", Definition: Definition{Goal: "Analyze feedback"}, Source: Source{"dispatch", "event-1/action-1"}, Input: "Analyze feedback"}
	return fixture{pool, NewStore(pool), p}
}

func TestTaskExplicitResumeRequiresCompletedRun(t *testing.T) {
	for _, terminal := range []State{StateSucceeded, StateFailed, StateCancelled} {
		t.Run(string(terminal), func(t *testing.T) {
			f := database(t)
			ctx := context.Background()
			task := createTask(t, f)
			run, err := f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"host", "start"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version})
			if err != nil {
				t.Fatal(err)
			}
			task, _, err = f.store.RecordResult(ctx, task.Scope, task.ID, ResultParams{Source: Source{"runtime", "terminal"}, RunID: run.ID, State: terminal})
			if err != nil {
				t.Fatal(err)
			}
			p := ResumeParams{Source: Source{"human", "continuation"}, ActorRef: "dingtalk:requester", Body: "Continue with delivery dates", ExpectedVersion: task.Version}
			resumed, entry, err := f.store.Resume(ctx, task.Scope, task.ID, p)
			if terminal != StateSucceeded {
				if !errors.Is(err, ErrRunNotReady) {
					t.Fatalf("unproven runner termination: %v", err)
				}
				return
			}
			if err != nil || resumed.State != StateReady || resumed.GoalRevision != task.GoalRevision || entry.Kind != "resumed" {
				t.Fatalf("resume: %+v %+v %v", resumed, entry, err)
			}
			again, replayed, err := f.store.Resume(ctx, task.Scope, task.ID, p)
			if err != nil || again.Version != resumed.Version || replayed.Seq != entry.Seq {
				t.Fatalf("replay: %+v %v", again, err)
			}
		})
	}
}
func applyMigration(t *testing.T, pool *pgxpool.Pool, path string) {
	t.Helper()
	sql, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(context.Background(), string(sql)); err != nil {
		t.Fatalf("apply %s: %v", filepath.Base(path), err)
	}
}
func createTask(t *testing.T, f fixture) Task {
	t.Helper()
	task, err := f.store.Create(context.Background(), f.params)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestTaskConcurrentCreateAndSourceConflict(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan Task, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { task, err := f.store.Create(ctx, f.params); results <- task; errs <- err })
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	id := ""
	for task := range results {
		if id != "" && id != task.ID {
			t.Fatal("source replay created two tasks")
		}
		id = task.ID
		if task.GoalRevision != 1 || task.Version != 1 || task.LastEntrySeq != 1 || task.State != StateReady {
			t.Fatalf("bad snapshot: %+v", task)
		}
	}
	entries, err := f.store.ReadEntries(ctx, f.params.Scope, id, 0, 100)
	if err != nil || len(entries) != 1 || entries[0].Kind != "request" {
		t.Fatalf("ledger: %+v %v", entries, err)
	}
	conflict := f.params
	conflict.Definition.Goal = "Different work"
	if _, err = f.store.Create(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed source payload: %v", err)
	}
	other := f.params
	other.Source.Key = "event-1/action-2"
	second, err := f.store.Create(ctx, other)
	if err != nil || second.ID == id {
		t.Fatalf("second action: %+v %v", second, err)
	}
}

func TestTaskInputCASAndCorrection(t *testing.T) {
	f := database(t)
	task := createTask(t, f)
	ctx := context.Background()
	info := InputParams{Source: Source{"dispatch", "question"}, ActorRef: "human:a", Body: "How far along?", ExpectedVersion: task.Version}
	updated, entry, err := f.store.AppendInput(ctx, task.Scope, task.ID, info)
	if err != nil || updated.GoalRevision != 1 || updated.Version != 2 || entry.Kind != "input" {
		t.Fatalf("information changed goal: %+v %+v %v", updated, entry, err)
	}
	replay, replayed, err := f.store.AppendInput(ctx, task.Scope, task.ID, info)
	if err != nil || replay.Version != 2 || replayed.Seq != entry.Seq {
		t.Fatalf("retry: %+v %+v %v", replay, replayed, err)
	}
	info.Body = "Changed payload"
	if _, _, err = f.store.AppendInput(ctx, task.Scope, task.ID, info); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting retry: %v", err)
	}
	corrected := Definition{Goal: "Exclude test customers"}
	correction := InputParams{Source: Source{"dispatch", "correction"}, ActorRef: "human:a", Body: "Exclude test customers", Correction: &corrected, ExpectedVersion: 1}
	if _, _, err = f.store.AppendInput(ctx, task.Scope, task.ID, correction); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale version: %v", err)
	}
	correction.ExpectedVersion = updated.Version
	updated, entry, err = f.store.AppendInput(ctx, task.Scope, task.ID, correction)
	if err != nil || updated.GoalRevision != 2 || entry.Kind != "amendment" || updated.Definition.Goal != corrected.Goal {
		t.Fatalf("correction: %+v %+v %v", updated, entry, err)
	}
	entries, err := f.store.ReadEntries(ctx, task.Scope, task.ID, 1, 1)
	if err != nil || len(entries) != 1 || entries[0].Seq != 2 || entries[0].Body != "How far along?" {
		t.Fatalf("append-only paged ledger: %+v %v", entries, err)
	}
}

func TestTaskConcurrentCAS(t *testing.T) {
	f := database(t)
	task := createTask(t, f)
	ctx := context.Background()
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, key := range []string{"a", "b"} {
		wg.Go(func() {
			_, _, err := f.store.AppendInput(ctx, task.Scope, task.ID, InputParams{Source: Source{"dispatch", key}, Body: key, ExpectedVersion: task.Version})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrConflict):
			conflict++
		default:
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("CAS success=%d conflict=%d", success, conflict)
	}
}

func TestRunSingleWriterAndLateResult(t *testing.T) {
	f := database(t)
	task := createTask(t, f)
	ctx := context.Background()
	start := StartRunParams{Source: Source{"host", "run-1"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version}
	run, err := f.store.StartRun(ctx, task.Scope, task.ID, start)
	if err != nil || run.GoalRevision != 1 || run.InputSeq != 1 || run.QueueTaskID != start.QueueTaskID {
		t.Fatalf("start: %+v %v", run, err)
	}
	again, err := f.store.StartRun(ctx, task.Scope, task.ID, start)
	if err != nil || again.ID != run.ID {
		t.Fatalf("start replay: %+v %v", again, err)
	}
	current, err := f.store.Get(ctx, task.Scope, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != StateRunning || current.ActiveRunID != run.ID {
		t.Fatalf("start snapshot: %+v", current)
	}
	other := StartRunParams{Source: Source{"host", "run-2"}, QueueTaskID: uuid.NewString(), ExpectedVersion: current.Version}
	if _, err = f.store.StartRun(ctx, task.Scope, task.ID, other); !errors.Is(err, ErrActiveRun) {
		t.Fatalf("competing run allowed: %v", err)
	}
	corrected := Definition{Goal: "Exclude test customers"}
	current, _, err = f.store.AppendInput(ctx, task.Scope, task.ID, InputParams{Source: Source{"dispatch", "correction"}, ActorRef: "human:a", Body: "Exclude test customers", Correction: &corrected, ExpectedVersion: current.Version})
	if err != nil {
		t.Fatal(err)
	}
	result := ResultParams{Source: Source{"runtime", "result-1"}, RunID: run.ID, State: StateSucceeded, Result: "Old result", ResultRef: "artifact:old"}
	current, finished, err := f.store.RecordResult(ctx, task.Scope, task.ID, result)
	if err != nil || finished.State != StateSucceeded || current.State != StateReady || current.GoalRevision != 2 || current.ActiveRunID != "" {
		t.Fatalf("old completion crossed correction: %+v %+v %v", current, finished, err)
	}
	replay, _, err := f.store.RecordResult(ctx, task.Scope, task.ID, result)
	if err != nil || replay.Version != current.Version {
		t.Fatalf("result replay changed snapshot: %+v %v", replay, err)
	}
	other.ExpectedVersion = current.Version
	fresh, err := f.store.StartRun(ctx, task.Scope, task.ID, other)
	if err != nil {
		t.Fatal(err)
	}
	current, _, err = f.store.RecordResult(ctx, task.Scope, task.ID, ResultParams{Source: Source{"runtime", "result-2"}, RunID: fresh.ID, State: StateSucceeded, Result: "Correct result"})
	if err != nil || current.State != StateSucceeded {
		t.Fatalf("new revision did not complete: %+v %v", current, err)
	}
	entries, err := f.store.ReadEntries(ctx, task.Scope, task.ID, 0, 100)
	if err != nil || len(entries) != 6 || entries[3].Body != "Old result" || entries[3].GoalRevision != 1 {
		t.Fatalf("late result history: %+v %v", entries, err)
	}
}

func TestRunConcurrentStart(t *testing.T) {
	f := database(t)
	task := createTask(t, f)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, key := range []string{"a", "b"} {
		wg.Go(func() {
			_, err := f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"host", key}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrActiveRun) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("active writers=%d", success)
	}
}

func TestTaskEveryOperationEnforcesScope(t *testing.T) {
	f := database(t)
	task := createTask(t, f)
	ctx := context.Background()
	run, err := f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"host", "run"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Scope){func(s *Scope) { s.WorkspaceID = uuid.NewString() }, func(s *Scope) { s.AgentID = uuid.NewString() }, func(s *Scope) { s.TenantOrgID = "org-b" }, func(s *Scope) { s.Scene.SceneID = uuid.NewString() }} {
		scope := task.Scope
		mutate(&scope)
		if _, err := f.store.Get(ctx, scope, task.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get crossed scope: %v", err)
		}
		if _, _, err := f.store.AppendInput(ctx, scope, task.ID, InputParams{Source: Source{"dispatch", "input"}, Body: "private", ExpectedVersion: 2}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("AppendInput crossed scope: %v", err)
		}
		if _, err := f.store.StartRun(ctx, scope, task.ID, StartRunParams{Source: Source{"host", "other"}, QueueTaskID: uuid.NewString(), ExpectedVersion: 2}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("StartRun crossed scope: %v", err)
		}
		if _, _, err := f.store.RecordResult(ctx, scope, task.ID, ResultParams{Source: Source{"runtime", "result"}, RunID: run.ID, State: StateSucceeded}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("RecordResult crossed scope: %v", err)
		}
		if _, err := f.store.BindIssue(ctx, scope, task.ID, BindIssueParams{Source: Source{"host", "bind"}, IssueID: uuid.NewString(), ExpectedVersion: 2}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("BindIssue crossed scope: %v", err)
		}
		if _, err := f.store.ReadEntries(ctx, scope, task.ID, 0, 100); !errors.Is(err, ErrNotFound) {
			t.Fatalf("ReadEntries crossed scope: %v", err)
		}
	}
}

func TestTaskIssueBindingAndLegacyScope(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	f.params.Scope = Scope{WorkspaceID: f.params.Scope.WorkspaceID, AgentID: f.params.Scope.AgentID, Kind: ScopeLegacyIssue, LegacyID: uuid.NewString()}
	f.params.OwnerLoop = LoopCoordinator
	f.params.DispatchMode = DispatchIssue
	task := createTask(t, f)
	binding := BindIssueParams{Source: Source{"host", "bind"}, IssueID: task.Scope.LegacyID, ExpectedVersion: task.Version}
	task, err := f.store.BindIssue(ctx, task.Scope, task.ID, binding)
	if err != nil || task.IssueID != binding.IssueID || task.Scope.Scene.SceneID != "" {
		t.Fatalf("legacy binding: %+v %v", task, err)
	}
	again, err := f.store.BindIssue(ctx, task.Scope, task.ID, binding)
	if err != nil || again.Version != task.Version {
		t.Fatalf("binding retry: %+v %v", again, err)
	}
	f.params.Source.Key = "another-action"
	other := createTask(t, f)
	binding.ExpectedVersion = other.Version
	if _, err = f.store.BindIssue(ctx, other.Scope, other.ID, binding); !errors.Is(err, ErrConflict) {
		t.Fatalf("issue bound twice: %v", err)
	}
	f.params.OwnerLoop = LoopEmployee
	f.params.Source.Key = "employee-legacy"
	if _, err = f.store.Create(ctx, f.params); !errors.Is(err, ErrInvalid) {
		t.Fatalf("employee accepted legacy scene: %v", err)
	}
}

func TestTaskAtomicLedgerAndOuterTransaction(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `ALTER TABLE employee_task_entry ADD CONSTRAINT injected_failure CHECK (body <> 'reject ledger')`)
	if err != nil {
		t.Fatal(err)
	}
	failing := f.params
	failing.Input = "reject ledger"
	if _, err = f.store.Create(ctx, failing); err == nil {
		t.Fatal("injected ledger failure ignored")
	}
	var count int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM employee_task`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("snapshot escaped rollback: %d %v", count, err)
	}
	task := createTask(t, f)
	if _, _, err = f.store.AppendInput(ctx, task.Scope, task.ID, InputParams{Source: Source{"dispatch", "bad"}, Body: "reject ledger", ExpectedVersion: task.Version}); err == nil {
		t.Fatal("injected append failure ignored")
	}
	got, err := f.store.Get(ctx, task.Scope, task.ID)
	if err != nil || got.Version != task.Version || got.LastEntrySeq != 1 {
		t.Fatalf("partial append: %+v %v", got, err)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	run, err := NewStore(tx).StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"host", "run"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = f.store.Get(ctx, task.Scope, task.ID)
	if err != nil || got.ActiveRunID != "" || got.Version != task.Version {
		t.Fatalf("run escaped outer rollback: %+v %v", got, err)
	}
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM employee_task_run WHERE id=$1`, run.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("mapping escaped rollback: %d %v", count, err)
	}
	recovered, err := NewStore(f.pool).Get(ctx, task.Scope, task.ID)
	if err != nil || recovered.ID != task.ID {
		t.Fatalf("committed work not recoverable: %+v %v", recovered, err)
	}
}

func TestTaskSourceDedupeIsScopedAndSceneMustExist(t *testing.T) {
	f := database(t)
	first := createTask(t, f)
	ctx := context.Background()
	ws, err := util.ParseUUID(first.Scope.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := util.ParseUUID(first.Scope.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	row, err := scene.Resolve(ctx, db.New(f.pool), scene.Owner{WorkspaceID: ws, AgentID: agent}, scene.DingTalkConversation(first.Scope.TenantOrgID, scene.KindDM, "cid-"+uuid.NewString()), scene.Observation{KindStated: true})
	if err != nil {
		t.Fatal(err)
	}
	secondParams := f.params
	secondParams.Scope.Scene = scene.RefOf(row)
	second, err := f.store.Create(ctx, secondParams)
	if err != nil || second.ID == first.ID {
		t.Fatalf("same key in another source scope was collapsed: %+v %v", second, err)
	}
	for _, scope := range []Scope{
		{WorkspaceID: first.Scope.WorkspaceID, AgentID: first.Scope.AgentID, TenantOrgID: first.Scope.TenantOrgID, Kind: ScopeScene, Scene: scene.Ref{SceneID: uuid.NewString()}},
		{WorkspaceID: first.Scope.WorkspaceID, AgentID: first.Scope.AgentID, TenantOrgID: "another-org", Kind: ScopeScene, Scene: first.Scope.Scene},
	} {
		rejected := f.params
		rejected.Scope = scope
		if _, err = f.store.Create(ctx, rejected); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unregistered/foreign scene accepted: %v", err)
		}
	}
}

func TestRunQueueMappingIsUniqueAndConflictingReplayRejected(t *testing.T) {
	f := database(t)
	task := createTask(t, f)
	ctx := context.Background()
	p := StartRunParams{Source: Source{"host", "run"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version}
	run, err := f.store.StartRun(ctx, task.Scope, task.ID, p)
	if err != nil {
		t.Fatal(err)
	}
	changed := p
	changed.QueueTaskID = uuid.NewString()
	if _, err = f.store.StartRun(ctx, task.Scope, task.ID, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed queue mapping replayed: %v", err)
	}
	f.params.Source.Key = "other-task"
	other := createTask(t, f)
	p.ExpectedVersion = other.Version
	if _, err = f.store.StartRun(ctx, other.Scope, other.ID, p); !errors.Is(err, ErrConflict) {
		t.Fatalf("one queue execution attached to two goals: %v", err)
	}
	result := ResultParams{Source: Source{"runtime", "completion"}, RunID: run.ID, State: StateSucceeded, Result: "original"}
	if _, _, err = f.store.RecordResult(ctx, task.Scope, task.ID, result); err != nil {
		t.Fatal(err)
	}
	result.Result = "overwritten"
	if _, _, err = f.store.RecordResult(ctx, task.Scope, task.ID, result); !errors.Is(err, ErrConflict) {
		t.Fatalf("completion payload overwritten: %v", err)
	}
	result.Source.Key = "different-callback"
	result.State = StateFailed
	if _, _, err = f.store.RecordResult(ctx, task.Scope, task.ID, result); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal run was rewritten: %v", err)
	}
}

// Observe a real PostgreSQL lock wait, so a goroutine that simply has not been
// scheduled cannot make the exclusion test pass.
func waitForWorkspaceLock(t *testing.T, pool *pgxpool.Pool, pid uint32, finished <-chan struct{}) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		var query string
		if err := pool.QueryRow(context.Background(), `SELECT cardinality(pg_blocking_pids(pid)) > 0, query FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&blocked, &query); err != nil {
			t.Fatal(err)
		}
		if blocked {
			if !strings.Contains(query, "FROM workspace WHERE id=") {
				t.Fatalf("operation locked a child before the workspace: %s", query)
			}
			return
		}
		select {
		case <-finished:
			t.Fatal("operation passed the workspace deletion lock")
		case <-deadline.C:
			t.Fatal("operation did not reach a PostgreSQL lock wait")
		case <-tick.C:
		}
	}
}

func deleteEmployeeFixture(ctx context.Context, tx pgx.Tx, workspaceID string) error {
	for _, table := range []string{"employee_task_run", "employee_task_entry", "employee_task", "agent_scene"} {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE workspace_id=$1`, workspaceID); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `DELETE FROM workspace WHERE id=$1`, workspaceID)
	return err
}

func TestTaskWritesWaitForWorkspaceDeletionAndRejectDeletedParent(t *testing.T) {
	for _, operation := range []string{"create", "append", "start", "result", "bind"} {
		t.Run(operation, func(t *testing.T) {
			f := database(t)
			task := createTask(t, f)
			ctx := context.Background()
			var run Run
			if operation == "result" {
				var err error
				run, err = f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"host", "run"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version})
				if err != nil {
					t.Fatal(err)
				}
			}
			deletion, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = deletion.Rollback(context.Background()) })
			if _, err = deletion.Exec(ctx, `SELECT id FROM workspace WHERE id=$1 FOR UPDATE`, task.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
			writer, err := f.pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			pid := writer.Conn().PgConn().PID()
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			finished := make(chan struct{})
			result := make(chan error, 1)
			t.Cleanup(func() { cancel(); <-finished })
			go func() {
				defer close(finished)
				defer writer.Release()
				store := NewStore(writer)
				var err error
				switch operation {
				case "create":
					p := f.params
					p.Source.Key = "late-create"
					_, err = store.Create(writeCtx, p)
				case "append":
					_, _, err = store.AppendInput(writeCtx, task.Scope, task.ID, InputParams{Source: Source{"host", "input"}, Body: "late input", ExpectedVersion: task.Version})
				case "start":
					_, err = store.StartRun(writeCtx, task.Scope, task.ID, StartRunParams{Source: Source{"host", "run"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version})
				case "result":
					_, _, err = store.RecordResult(writeCtx, task.Scope, task.ID, ResultParams{Source: Source{"host", "result"}, RunID: run.ID, State: StateSucceeded, Result: "late result"})
				case "bind":
					_, err = store.BindIssue(writeCtx, task.Scope, task.ID, BindIssueParams{Source: Source{"host", "issue"}, IssueID: uuid.NewString(), ExpectedVersion: task.Version})
				}
				result <- err
			}()
			waitForWorkspaceLock(t, f.pool, pid, finished)
			if err = deleteEmployeeFixture(ctx, deletion, task.Scope.WorkspaceID); err != nil {
				t.Fatal(err)
			}
			if err = deletion.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-result; !errors.Is(err, ErrNotFound) {
				t.Fatalf("write after parent deletion: %v", err)
			}
			for _, table := range []string{"employee_task", "employee_task_entry", "employee_task_run"} {
				var count int
				if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE workspace_id=$1`, task.Scope.WorkspaceID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("orphan %s rows=%d error=%v", table, count, err)
				}
			}
		})
	}
}

func TestTaskOuterWriteTransactionBlocksWorkspaceDeletion(t *testing.T) {
	for _, operation := range []string{"create", "append"} {
		t.Run(operation, func(t *testing.T) {
			f := database(t)
			ctx := context.Background()
			var task Task
			if operation == "append" {
				task = createTask(t, f)
			}
			write, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = write.Rollback(context.Background()) })
			if operation == "create" {
				_, err = NewStore(write).Create(ctx, f.params)
			} else {
				_, _, err = NewStore(write).AppendInput(ctx, task.Scope, task.ID, InputParams{Source: Source{"host", "input"}, Body: "committing input", ExpectedVersion: task.Version})
			}
			if err != nil {
				t.Fatal(err)
			}
			deleter, err := f.pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			pid := deleter.Conn().PgConn().PID()
			deleteCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			finished := make(chan struct{})
			result := make(chan error, 1)
			t.Cleanup(func() { cancel(); <-finished })
			go func() {
				defer close(finished)
				defer deleter.Release()
				tx, err := deleter.Begin(deleteCtx)
				if err != nil {
					result <- err
					return
				}
				defer tx.Rollback(deleteCtx)
				_, err = tx.Exec(deleteCtx, `SELECT id FROM workspace WHERE id=$1 FOR UPDATE`, f.params.Scope.WorkspaceID)
				if err == nil {
					err = deleteEmployeeFixture(deleteCtx, tx, f.params.Scope.WorkspaceID)
				}
				if err == nil {
					err = tx.Commit(deleteCtx)
				}
				result <- err
			}()
			waitForWorkspaceLock(t, f.pool, pid, finished)
			if err = write.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-result; err != nil {
				t.Fatal(err)
			}
			var count int
			if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM employee_task WHERE workspace_id=$1`, f.params.Scope.WorkspaceID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("task escaped concurrent teardown: %d %v", count, err)
			}
		})
	}
}

func TestRunRetainsAcceptedInputBoundary(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := createTask(t, f)
	task, _, err := f.store.AppendInput(ctx, task.Scope, task.ID, InputParams{Source: Source{"event", "later"}, Body: "Not part of the already accepted queue", ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"host", "accepted"}, QueueTaskID: uuid.NewString(), InputSeq: 1, ExpectedVersion: task.Version})
	if err != nil || run.InputSeq != 1 {
		t.Fatalf("accepted boundary: %+v %v", run, err)
	}
}

func TestBackendRunLedgerDoesNotRequireIssueOrQueueTables(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	var absent bool
	if err := f.pool.QueryRow(ctx, `SELECT to_regclass('issue') IS NULL AND to_regclass('agent_task_queue') IS NULL`).Scan(&absent); err != nil || !absent {
		t.Fatalf("core fixture unexpectedly depends on backend tables: %v %v", absent, err)
	}
	f.params.OwnerLoop = LoopCoordinator
	f.params.DispatchMode = DispatchIssue
	task := createTask(t, f)
	task, err := f.store.BindIssue(ctx, task.Scope, task.ID, BindIssueParams{Source: Source{"backend", "binding"}, IssueID: uuid.NewString(), ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	params := ObserveBackendRunParams{Source: Source{"backend", "accepted-run"}, QueueTaskID: uuid.NewString(), GoalRevision: task.GoalRevision, InputSeq: task.LastEntrySeq, ExpectedVersion: task.Version}
	run, err := f.store.ObserveBackendRun(ctx, task.Scope, task.ID, params)
	if err != nil {
		t.Fatalf("backend-independent ledger rejected an accepted execution fact: %v", err)
	}
	replay, err := f.store.ObserveBackendRun(ctx, task.Scope, task.ID, params)
	if err != nil || replay.ID != run.ID {
		t.Fatalf("replayed fact: %+v %v", replay, err)
	}
	result := ResultParams{Source: Source{"backend", "terminal"}, RunID: run.ID, State: StateSucceeded, Result: "BACKEND_FACT", ResultRef: "backend-result:test"}
	finished, _, err := f.store.RecordResult(ctx, task.Scope, task.ID, result)
	if err != nil {
		t.Fatal(err)
	}
	again, _, err := f.store.RecordResult(ctx, task.Scope, task.ID, result)
	if err != nil || again.Version != finished.Version || again.State != StateSucceeded {
		t.Fatalf("terminal replay: %+v %v", again, err)
	}
	if _, err := f.store.ReadEntries(ctx, task.Scope, task.ID, 0, 20); err != nil {
		t.Fatal(err)
	}
}

func TestObserveBackendRunRetainsAggregateGuards(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	direct := createTask(t, f)
	params := ObserveBackendRunParams{Source: Source{"backend", "observed"}, QueueTaskID: uuid.NewString(), GoalRevision: 1, InputSeq: 1, ExpectedVersion: direct.Version}
	if _, err := f.store.ObserveBackendRun(ctx, direct.Scope, direct.ID, params); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Direct accepted backend observation: %v", err)
	}
	f.params.Source.Key = "coordinator-goal"
	f.params.OwnerLoop = LoopCoordinator
	f.params.DispatchMode = DispatchIssue
	task := createTask(t, f)
	if _, err := f.store.ObserveBackendRun(ctx, task.Scope, task.ID, params); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unbound task accepted backend observation: %v", err)
	}
	task, err := f.store.BindIssue(ctx, task.Scope, task.ID, BindIssueParams{Source: Source{"host", "issue"}, IssueID: uuid.NewString(), ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	params.ExpectedVersion = task.Version
	future := params
	future.InputSeq = task.LastEntrySeq + 1
	if _, err := f.store.ObserveBackendRun(ctx, task.Scope, task.ID, future); !errors.Is(err, ErrInvalid) {
		t.Fatalf("future input admitted: %v", err)
	}
	future = params
	future.GoalRevision = task.GoalRevision + 1
	if _, err := f.store.ObserveBackendRun(ctx, task.Scope, task.ID, future); !errors.Is(err, ErrInvalid) {
		t.Fatalf("future revision admitted: %v", err)
	}
	observed, err := f.store.ObserveBackendRun(ctx, task.Scope, task.ID, params)
	if err != nil || observed.QueueTaskID != params.QueueTaskID {
		t.Fatalf("accepted backend observation: %+v %v", observed, err)
	}
	again, err := f.store.ObserveBackendRun(ctx, task.Scope, task.ID, params)
	if err != nil || again.ID != observed.ID {
		t.Fatalf("observation replay: %+v %v", again, err)
	}
	current, err := f.store.Get(ctx, task.Scope, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	params.Source.Key = "second-run"
	params.QueueTaskID = uuid.NewString()
	params.ExpectedVersion = current.Version
	if _, err := f.store.ObserveBackendRun(ctx, task.Scope, task.ID, params); !errors.Is(err, ErrActiveRun) {
		t.Fatalf("second writer admitted: %v", err)
	}
}

func TestDirectStartRetainsRunnerFenceAcrossCorrectionsAndLateResults(t *testing.T) {
	for _, terminal := range []State{StateFailed, StateCancelled} {
		for _, correctionFirst := range []bool{false, true} {
			name := string(terminal) + "_then_correction"
			if correctionFirst {
				name = string(terminal) + "_after_correction"
			}
			t.Run(name, func(t *testing.T) {
				f := database(t)
				ctx := context.Background()
				task := createTask(t, f)
				firstStart := StartRunParams{Source: Source{"host", "first"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version}
				first, err := f.store.StartRun(ctx, task.Scope, task.ID, firstStart)
				if err != nil {
					t.Fatal(err)
				}
				task, err = f.store.Get(ctx, task.Scope, task.ID)
				if err != nil {
					t.Fatal(err)
				}
				correct := func() {
					updated := Definition{Goal: "The corrected goal"}
					task, _, err = f.store.AppendInput(ctx, task.Scope, task.ID, InputParams{Source: Source{"human", "correction"}, ActorRef: "member:requester", Body: "Use the corrected goal", Correction: &updated, ExpectedVersion: task.Version})
					if err != nil {
						t.Fatal(err)
					}
				}
				if correctionFirst {
					correct()
				}
				task, _, err = f.store.RecordResult(ctx, task.Scope, task.ID, ResultParams{Source: Source{"runtime", "terminal"}, RunID: first.ID, State: terminal, Result: "No confirmed process termination"})
				if err != nil {
					t.Fatal(err)
				}
				if !correctionFirst {
					correct()
				}
				before := task
				if _, _, err = f.store.RecordResult(ctx, task.Scope, task.ID, ResultParams{Source: Source{"runtime", "late-success"}, RunID: first.ID, State: StateSucceeded, Result: "Late success"}); !errors.Is(err, ErrConflict) {
					t.Fatalf("late success rewrote terminal Run: %v", err)
				}
				if _, err = f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"host", "second"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version}); !errors.Is(err, ErrRunNotReady) {
					t.Fatalf("new Direct writer admitted after %s and correction: %v", terminal, err)
				}
				if _, err := f.store.ObserveBackendRun(ctx, task.Scope, task.ID, ObserveBackendRunParams{Source: Source{"backend", "bypass-stop"}, QueueTaskID: uuid.NewString(), GoalRevision: task.GoalRevision, InputSeq: task.LastEntrySeq, ExpectedVersion: task.Version}); !errors.Is(err, ErrInvalid) {
					t.Fatalf("Direct stop fence bypassed through backend observation: %v", err)
				}
				replay, err := f.store.StartRun(ctx, task.Scope, task.ID, firstStart)
				if err != nil || replay.ID != first.ID {
					t.Fatalf("existing Run replay was blocked: %+v %v", replay, err)
				}
				got, err := f.store.Get(ctx, task.Scope, task.ID)
				if err != nil || got.Version != before.Version || got.State != before.State || got.ActiveRunID != "" {
					t.Fatalf("rejected start changed snapshot: %+v %v", got, err)
				}
				var runs int
				if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM employee_task_run WHERE task_id=$1::uuid`, task.ID).Scan(&runs); err != nil || runs != 1 {
					t.Fatalf("second writer persisted: runs=%d %v", runs, err)
				}
			})
		}
	}
}

func TestDirectCompletedRunCanExplicitlyResumeAndStartAgain(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task := createTask(t, f)
	run, err := f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"host", "first"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	task, _, err = f.store.RecordResult(ctx, task.Scope, task.ID, ResultParams{Source: Source{"runtime", "completed"}, RunID: run.ID, State: StateSucceeded, Result: "Execution completed"})
	if err != nil {
		t.Fatal(err)
	}
	task, _, err = f.store.Resume(ctx, task.Scope, task.ID, ResumeParams{Source: Source{"human", "continue"}, ActorRef: "member:requester", Body: "Continue this goal", ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	next, err := f.store.StartRun(ctx, task.Scope, task.ID, StartRunParams{Source: Source{"host", "second"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version})
	if err != nil || next.ID == run.ID || next.GoalRevision != run.GoalRevision {
		t.Fatalf("completed continuation rejected: %+v %v", next, err)
	}
}
