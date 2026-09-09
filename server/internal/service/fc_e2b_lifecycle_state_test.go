package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/events"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type lifecycleStateFixture struct {
	pool *pgxpool.Pool
	queries *db.Queries
	execution db.AgentTaskRuntimeStartAttempt
	workspaceID pgtype.UUID
	issueID pgtype.UUID
}

func newLifecycleStateFixture(t *testing.T) lifecycleStateFixture {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" { t.Skip("DATABASE_URL not set") }
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil { t.Fatal("invalid test database configuration") }
	t.Cleanup(pool.Close)
	workspaceID, userID, runtimeID := seedFCE2BSandboxRuntime(t, pool, "Lifecycle state test")
	var agentID, issueID, taskID pgtype.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO agent (workspace_id, name, runtime_mode, runtime_id, owner_id)
VALUES ($1, 'Lifecycle agent', 'cloud', $2, $3) RETURNING id`, workspaceID, runtimeID, userID).Scan(&agentID); err != nil { t.Fatal(err) }
	if err := pool.QueryRow(ctx, `INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, position)
VALUES ($1, 'Lifecycle issue', 'in_progress', 'none', $2, 'member', 1, 0) RETURNING id`, workspaceID, userID).Scan(&issueID); err != nil { t.Fatal(err) }
	if err := pool.QueryRow(ctx, `INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, started_at)
VALUES ($1, $2, $3, 'running', 0, now() - interval '30 minutes') RETURNING id`, agentID, runtimeID, issueID).Scan(&taskID); err != nil { t.Fatal(err) }
	attemptID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	if _, err := pool.Exec(ctx, `INSERT INTO agent_task_runtime_start_attempt (id, task_id, runtime_id, backend, protocol, sandbox_id, status)
VALUES ($1, $2, $3, 'aliyun_fc', 'legacy-v1', 'sbx-lifecycle', 'claimed')`, attemptID, taskID, runtimeID); err != nil { t.Fatal(err) }
	queries := db.New(pool)
	if _, err := queries.UpsertFCE2BSandboxSession(ctx, db.UpsertFCE2BSandboxSessionParams{
		WorkspaceID: workspaceID, RuntimeID: runtimeID, ScopeType: "issue", ScopeID: issueID,
		SandboxID: "sbx-lifecycle", Template: "tpl-test", ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Minute), Valid: true},
	}); err != nil { t.Fatal(err) }
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM agent_task_runtime_start_attempt WHERE task_id = $1`, taskID)
		pool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		pool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, issueID)
		pool.Exec(ctx, `DELETE FROM agent WHERE id = $1`, agentID)
		pool.Exec(ctx, `DELETE FROM fc_e2b_sandbox_session WHERE runtime_id = $1`, runtimeID)
		pool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})
	return lifecycleStateFixture{pool, queries, db.AgentTaskRuntimeStartAttempt{
		ID: attemptID, TaskID: taskID, RuntimeID: runtimeID, SandboxID: "sbx-lifecycle", Backend: "aliyun_fc", Status: "claimed",
	}, workspaceID, issueID}
}

func (f lifecycleStateFixture) launcher(apiURL string) *FCE2BLauncher {
	return &FCE2BLauncher{
		Pool: f.pool, Queries: f.queries,
		Tasks: NewTaskService(f.queries, f.pool, nil, events.New()),
		Config: FCE2BConfig{Enabled: true, SandboxRenewalEnabled: true, APIURL: apiURL, APIKey: "test-key", TimeoutSeconds: 3600},
	}
}

func TestFCE2BLifecycleUsesExistingTaskStateWithoutEnrollment(t *testing.T) {
	var mu sync.Mutex
	requests, posts := 0, 0
	end := time.Now().Add(time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		if r.Method == http.MethodPost { posts++; end = time.Now().Add(time.Hour); w.WriteHeader(204); return }
		_ = json.NewEncoder(w).Encode(fcE2BSandboxInfo{ID: "sbx-lifecycle", State: "running", EndAt: end})
	}))
	defer server.Close()
	f := newLifecycleStateFixture(t)
	launcher := f.launcher(server.URL)
	ctx := context.Background()
	if err := launcher.checkSandboxExecutions(ctx); err != nil { t.Fatal("check existing running task failed:", err) }
	mu.Lock()
	if posts != 1 { t.Errorf("running task renewed %d times, want 1", posts) }
	firstRequests, expected := requests, end
	mu.Unlock()
	var actual time.Time
	if err := f.pool.QueryRow(ctx, `SELECT expires_at FROM fc_e2b_sandbox_session WHERE runtime_id = $1`, f.execution.RuntimeID).Scan(&actual); err != nil { t.Fatal(err) }
	if actual.Sub(expected).Abs() > time.Millisecond { t.Fatal("existing sandbox session expiry was not updated") }
	if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'completed' WHERE id = $1`, f.execution.TaskID); err != nil { t.Fatal(err) }
	if err := launcher.checkSandboxExecutions(ctx); err != nil { t.Fatal(err) }
	mu.Lock()
	defer mu.Unlock()
	if requests != firstRequests { t.Fatal("completed task still contacted the provider") }
}

func TestFCE2BLifecycleSelectsCurrentActiveTasks(t *testing.T) {
	for _, status := range []string{"running", "dispatched", "waiting_local_directory", "queued", "completed", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			f := newLifecycleStateFixture(t)
			ctx := context.Background()
			if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET status = $2 WHERE id = $1`, f.execution.TaskID, status); err != nil { t.Fatal(err) }
			// An active execution can outlive or lack its warm-reuse mapping.
			if _, err := f.pool.Exec(ctx, `DELETE FROM fc_e2b_sandbox_session WHERE runtime_id = $1`, f.execution.RuntimeID); err != nil { t.Fatal(err) }
			rows, err := f.queries.ListFCE2BSandboxExecutionChecks(ctx)
			if err != nil { t.Fatal(err) }
			found := false
			for _, row := range rows { if row.ID == f.execution.ID { found = true } }
			active := status == "running" || status == "dispatched" || status == "waiting_local_directory"
			if found != active { t.Fatalf("status %s selected=%v, want %v", status, found, active) }
		})
	}
}

func TestFCE2BLifecycleMissingAndTransientFailures(t *testing.T) {
	for _, code := range []int{404, 503} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
			defer server.Close()
			f := newLifecycleStateFixture(t)
			err := f.launcher(server.URL).checkSandboxExecution(context.Background(), f.execution)
			if code == 404 && err != nil { t.Fatal(err) }
			if code == 503 && err == nil { t.Fatal("transient failure was hidden") }
			task, err := f.queries.GetAgentTask(context.Background(), f.execution.TaskID)
			if err != nil { t.Fatal(err) }
			if code == 404 && (task.Status != "failed" || task.FailureReason.String != "sandbox_expired") { t.Fatalf("missing sandbox left task %s / %s", task.Status, task.FailureReason.String) }
			if code == 503 && task.Status != "running" { t.Fatal("transient provider failure ended task") }
		})
	}
}

func TestFCE2BLifecycleDoesNotOverwriteReplacement(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(404) }))
	defer server.Close()
	f := newLifecycleStateFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `INSERT INTO agent_task_runtime_start_attempt (id, task_id, runtime_id, backend, protocol, sandbox_id)
VALUES ($1, $2, $3, 'aliyun_fc', 'legacy-v1', 'sbx-replacement')`, uuid.NewString(), f.execution.TaskID, f.execution.RuntimeID); err != nil { t.Fatal(err) }
	rows, err := f.queries.ListFCE2BSandboxExecutionChecks(ctx)
	if err != nil { t.Fatal(err) }
	for _, row := range rows { if row.ID == f.execution.ID { t.Fatal("old attempt was still selected") } }
	launcher := f.launcher(server.URL)
	if err := launcher.checkSandboxExecution(ctx, f.execution); err != nil { t.Fatal(err) }
	if calls.Load() != 0 { t.Fatal("superseded execution contacted provider") }
	_, err = launcher.Tasks.failTask(ctx, f.execution.TaskID, "old sandbox gone", "", "", "sandbox_expired", false, "", failTaskOptions{fcE2BExecution: &f.execution})
	if err == nil { t.Fatal("stale execution failure was accepted") }
	task, err := f.queries.GetAgentTask(ctx, f.execution.TaskID)
	if err != nil || task.Status != "running" { t.Fatal("replacement task was failed", err) }
}

func TestFCE2BLifecycleRechecksTaskAfterProviderLookup(t *testing.T) {
	f := newLifecycleStateFixture(t)
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost { posts.Add(1); w.WriteHeader(204); return }
		if _, err := f.pool.Exec(r.Context(), `UPDATE agent_task_queue SET status = 'completed' WHERE id = $1`, f.execution.TaskID); err != nil { t.Error(err) }
		_ = json.NewEncoder(w).Encode(fcE2BSandboxInfo{ID: "sbx-lifecycle", State: "running", EndAt: time.Now().Add(time.Minute)})
	}))
	defer server.Close()
	if err := f.launcher(server.URL).checkSandboxExecution(context.Background(), f.execution); err != nil { t.Fatal(err) }
	if posts.Load() != 0 { t.Fatal("task completed during lookup but was renewed") }
}

func TestFCE2BWarmRenewalAndSweepShareInstanceLock(t *testing.T) {
	var mutex sync.Mutex
	posts := 0
	end := time.Now().Add(time.Minute)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		if r.Method == http.MethodPost { posts++; end = time.Now().Add(time.Hour); w.WriteHeader(204); return }
		_ = json.NewEncoder(w).Encode(fcE2BSandboxInfo{ID: "sbx-lifecycle", State: "running", EndAt: end})
	}))
	defer server.Close()
	f := newLifecycleStateFixture(t)
	launcher := f.launcher(server.URL)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); if err := launcher.ensureSandboxLifetime(context.Background(), f.execution.RuntimeID, f.execution.SandboxID, nil); err != nil { t.Error(err) } }()
	go func() { defer wg.Done(); if err := launcher.checkSandboxExecution(context.Background(), f.execution); err != nil { t.Error(err) } }()
	wg.Wait()
	mutex.Lock()
	defer mutex.Unlock()
	if posts != 1 { t.Fatalf("concurrent warm reuse and sweep renewed %d times", posts) }
}

func TestFCE2BWarmReuseChecksProviderWhenDatabaseExpiryIsStale(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(fcE2BSandboxInfo{ID: "sbx-lifecycle", State: "running", EndAt: time.Now().Add(time.Hour)})
	}))
	defer server.Close()
	f := newLifecycleStateFixture(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE fc_e2b_sandbox_session SET expires_at = now() - interval '1 minute' WHERE runtime_id = $1`, f.execution.RuntimeID); err != nil { t.Fatal(err) }
	launcher := f.launcher(server.URL)
	launcher.LifecycleRedis = lifecycleRedisClient(t)
	runner := &countingRunner{}
	launcher.Runner = runner
	id, cold, err := launcher.resolveSandbox(ctx, db.AgentRuntime{ID: f.execution.RuntimeID, WorkspaceID: f.workspaceID},
		fcE2BTaskScope{typ: "issue", id: f.issueID}, true, "tpl-test", chattrace.New("test"))
	if err != nil || cold || id != f.execution.SandboxID || runner.createCount() != 0 {
		t.Fatalf("lost live warm sandbox after stale database expiry: id=%s cold=%v creates=%d err=%v", id, cold, runner.createCount(), err)
	}
}

type lifecycleTestLease struct { acquired bool; finished bool }
func (l *lifecycleTestLease) claim(context.Context) (string, bool, error) { return "test", l.acquired, nil }
func (l *lifecycleTestLease) renew(context.Context, string) error { return nil }
func (l *lifecycleTestLease) finish(context.Context, string) error { l.finished = true; return nil }

func TestFCE2BLifecycleRoundRequiresLeaseAndStopsOnCancellation(t *testing.T) {
	lease := &lifecycleTestLease{}
	if err := runFCE2BCheckRound(context.Background(), lease, func(context.Context) error { t.Fatal("unowned round executed"); return nil }); err != nil { t.Fatal(err) }
	ctx, cancel := context.WithCancel(context.Background())
	lease.acquired = true
	err := runFCE2BCheckRound(ctx, lease, func(context.Context) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) || lease.finished { t.Fatal("cancelled round released an uncertain lease", err) }
}
