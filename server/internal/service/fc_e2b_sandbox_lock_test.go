package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/chattrace"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// countingRunner records every `sandbox create` and hands back a distinct id,
// so a duplicate boot is visible rather than silently idempotent.
type countingRunner struct {
	mu        sync.Mutex
	creates   int
	createErr error
	// bootDelay widens the create window. Without it the first replica can
	// finish before the second even reads, and the race the lock exists to
	// close would not reproduce even unlocked.
	bootDelay time.Duration
}

func (r *countingRunner) Run(_ context.Context, _ string, args []string, _ []string) (string, error) {
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "sandbox create"):
		r.mu.Lock()
		r.creates++
		id := fmt.Sprintf("sbx_%d", r.creates)
		delay := r.bootDelay
		createErr := r.createErr
		r.mu.Unlock()
		if createErr != nil {
			return "", createErr
		}
		time.Sleep(delay)
		return "Sandbox created with ID " + id + " using template tpl_test", nil
	case strings.Contains(joined, "sandbox exec"):
		// Readiness probe: report the sandbox as alive.
		return "", nil
	default:
		return "", nil
	}
}

func (r *countingRunner) createCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.creates
}

func newSandboxLockPool(t *testing.T) *pgxpool.Pool {
	return newSandboxLockPoolWithMaxConns(t, 4)
}

func newSandboxLockPoolWithMaxConns(t *testing.T, maxConns int32) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	config, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Skipf("database config unavailable: %v", err)
	}
	config.MaxConns = maxConns
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Skipf("database not available: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("database not available: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedFCE2BSandboxRuntime(t *testing.T, pool *pgxpool.Pool, label string) (pgtype.UUID, pgtype.UUID, pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000000")
	var workspaceID, userID, runtimeID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, '', 'SBX') RETURNING id
	`, label, "sandbox-lock-"+suffix).Scan(&workspaceID); err != nil {
		t.Skipf("seed workspace: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO "user" (email, name) VALUES ($1, $2) RETURNING id
	`, "sandbox-lock-"+suffix+"@example.com", label).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, last_seen_at, visibility, owner_id
		)
		VALUES ($1, $2, $3, 'cloud', 'hermes', 'online',
			'', '{"kind":"fc-e2b","template":"tpl_old","template_id":"tpl_old_id"}'::jsonb,
			now(), 'private', $4)
		RETURNING id
	`, workspaceID, "fc-e2b:test:"+suffix, label, userID).Scan(&runtimeID); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID)
	})
	return workspaceID, userID, runtimeID
}

// The regression: the launch is driven by whichever replica served the enqueue
// (or the pending-chat poll, which the load balancer spreads freely), and the
// in-process launch guard only dedups within one replica. Both replicas missed
// the session row, both booted a sandbox, and the loser's Upsert clobbered the
// winner's — leaving an orphaned microVM billed until it timed out.
func TestResolveSandboxIsSerializedAcrossReplicas(t *testing.T) {
	pool := newSandboxLockPool(t)
	ctx := context.Background()
	queries := db.New(pool)

	// Fixtures: a workspace + a cloud runtime the sandbox session can reference.
	var workspaceID, userID, runtimeID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('Sandbox Lock Tests', $1, '', 'SBX') RETURNING id
	`, "sandbox-lock-"+time.Now().Format("150405.000000")).Scan(&workspaceID); err != nil {
		t.Skipf("seed workspace: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID) })

	if err := pool.QueryRow(ctx, `
		INSERT INTO "user" (email, name) VALUES ($1, 'Sandbox Lock Tester') RETURNING id
	`, "sandbox-lock-"+time.Now().Format("150405.000000")+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID) })

	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, last_seen_at, visibility, owner_id
		)
		VALUES ($1, NULL, 'Sandbox Lock Runtime', 'cloud', 'test', 'online',
			'', '{"kind":"fc-e2b"}'::jsonb, now(), 'private', $2)
		RETURNING id
	`, workspaceID, userID).Scan(&runtimeID); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}

	scope := fcE2BTaskScope{typ: "chat", id: runtimeID} // any stable scope id
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM fc_e2b_sandbox_session WHERE runtime_id = $1`, runtimeID)
	})

	runtime := db.AgentRuntime{ID: runtimeID, WorkspaceID: workspaceID}
	if _, err := queries.UpsertFCE2BSandboxSession(ctx, db.UpsertFCE2BSandboxSessionParams{
		WorkspaceID: workspaceID,
		RuntimeID:   runtimeID,
		ScopeType:   scope.typ,
		ScopeID:     scope.id,
		SandboxID:   "sbx_old_template",
		Template:    "tpl_old",
		ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("seed old-template sandbox: %v", err)
	}
	runner := &countingRunner{bootDelay: 300 * time.Millisecond}

	// Two launchers over one database = two replicas.
	newReplica := func() *FCE2BLauncher {
		l := NewFCE2BLauncher(queries, nil, FCE2BConfig{TimeoutSeconds: 300}, runner)
		l.SetPool(pool)
		return l
	}
	replicaA, replicaB := newReplica(), newReplica()

	var wg sync.WaitGroup
	start := make(chan struct{})
	ids := make([]string, 2)
	errs := make([]error, 2)
	for i, l := range []*FCE2BLauncher{replicaA, replicaB} {
		wg.Add(1)
		go func(i int, l *FCE2BLauncher) {
			defer wg.Done()
			<-start // both replicas enter the lookup-or-create together
			trace, traceErr := chattrace.From("37d0871a-3657-4c74-91fa-39e846fa90a0", "task", time.Now().UnixMilli())
			if traceErr != nil {
				errs[i] = traceErr
				return
			}
			id, _, err := l.resolveSandbox(ctx, runtime, scope, true, "tpl_test", trace)
			ids[i], errs[i] = id, err
		}(i, l)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("replica %d: resolveSandbox: %v", i, err)
		}
	}
	if got := runner.createCount(); got != 1 {
		t.Fatalf("booted %d sandboxes for one scope; the loser's is orphaned and billed", got)
	}
	if ids[0] != ids[1] {
		t.Errorf("replicas resolved different sandboxes (%q vs %q); one is orphaned", ids[0], ids[1])
	}
}

func TestResolveSandboxUnderRuntimeLockUsesSinglePoolConnection(t *testing.T) {
	pool := newSandboxLockPoolWithMaxConns(t, 1)
	workspaceID, _, runtimeID := seedFCE2BSandboxRuntime(t, pool, "Single Connection Sandbox Lock")
	queries := db.New(pool)
	runtime, err := queries.GetAgentRuntime(context.Background(), runtimeID)
	if err != nil {
		t.Fatalf("load runtime: %v", err)
	}

	runner := &countingRunner{}
	launcher := NewFCE2BLauncher(queries, nil, FCE2BConfig{TimeoutSeconds: 300}, runner)
	launcher.SetPool(pool)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, release, err := launcher.lockRuntimeShared(ctx, runtimeID)
	if err != nil {
		t.Fatalf("lock runtime: %v", err)
	}
	lockedLauncher := *launcher
	lockedLauncher.Queries = db.New(conn)
	scope := fcE2BTaskScope{typ: fcE2BScopeTypeChat, id: workspaceID}
	sandboxID, coldStart, err := lockedLauncher.resolveSandboxOnConnection(ctx, runtime, scope, true, "tpl_new", conn, chattrace.New("task"))
	release()
	if err != nil {
		t.Fatalf("resolve sandbox with one pool connection: %v", err)
	}
	if sandboxID != "sbx_1" || !coldStart {
		t.Fatalf("sandbox = (%q, cold=%v), want (sbx_1, true)", sandboxID, coldStart)
	}
	if runner.createCount() != 1 {
		t.Fatalf("sandbox creates = %d, want 1", runner.createCount())
	}

	acquireCtx, acquireCancel := context.WithTimeout(context.Background(), time.Second)
	defer acquireCancel()
	checkConn, err := pool.Acquire(acquireCtx)
	if err != nil {
		t.Fatalf("runtime lock connection was not returned to the pool: %v", err)
	}
	checkConn.Release()
}

func TestUpdateRuntimeTemplateWaitsForLaunchAndInvalidatesSessions(t *testing.T) {
	pool := newSandboxLockPoolWithMaxConns(t, 2)
	workspaceID, _, runtimeID := seedFCE2BSandboxRuntime(t, pool, "Template Rotation Lock")
	queries := db.New(pool)
	scopeID := workspaceID
	if _, err := queries.UpsertFCE2BSandboxSession(context.Background(), db.UpsertFCE2BSandboxSessionParams{
		WorkspaceID: workspaceID,
		RuntimeID:   runtimeID,
		ScopeType:   fcE2BScopeTypeChat,
		ScopeID:     scopeID,
		SandboxID:   "sbx_old",
		Template:    "tpl_old",
		ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("seed sandbox session: %v", err)
	}

	launcher := NewFCE2BLauncher(queries, nil, FCE2BConfig{}, &countingRunner{})
	launcher.SetPool(pool)
	_, release, err := launcher.lockRuntimeShared(context.Background(), runtimeID)
	if err != nil {
		t.Fatalf("hold launch lock: %v", err)
	}
	type updateOutcome struct {
		result FCE2BRuntimeTemplateUpdateResult
		err    error
	}
	done := make(chan updateOutcome, 1)
	go func() {
		result, err := launcher.UpdateRuntimeTemplate(context.Background(), runtimeID, FCE2BTemplate{
			ID:              "tpl_new_id",
			BuildID:         "build_new",
			Template:        "tpl_new",
			Name:            "New Template",
			Status:          "READY",
			ManifestVersion: 1,
			Providers:       []string{"hermes", "opencode", "pi"},
			Capabilities:    []string{"dws"},
			ComponentVersions: map[string]string{
				"hermes":   "0.19.0",
				"opencode": "v1.18.4",
				"pi":       "0.80.10",
				"dws":      "v1.0.53-beta.4",
			},
			RunnerProtocol: "root-log-v1",
		})
		done <- updateOutcome{result: result, err: err}
	}()
	select {
	case outcome := <-done:
		release()
		t.Fatalf("template update crossed an active launch lock: result=%+v err=%v", outcome.result, outcome.err)
	case <-time.After(150 * time.Millisecond):
	}
	release()

	var outcome updateOutcome
	select {
	case outcome = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("template update did not continue after launch lock release")
	}
	if outcome.err != nil {
		t.Fatalf("update runtime template: %v", outcome.err)
	}
	if !outcome.result.Changed || outcome.result.InvalidatedSandboxCount != 1 {
		t.Fatalf("update result = %+v, want changed with one invalidated sandbox", outcome.result)
	}
	var metadata map[string]any
	if err := json.Unmarshal(outcome.result.Runtime.Metadata, &metadata); err != nil {
		t.Fatalf("decode updated metadata: %v", err)
	}
	if metadata["template_id"] != "tpl_new_id" || metadata["template"] != "tpl_new" {
		t.Fatalf("updated metadata = %#v", metadata)
	}
	var status string
	if err := pool.QueryRow(context.Background(), `
		SELECT status FROM fc_e2b_sandbox_session
		WHERE runtime_id = $1 AND scope_type = $2 AND scope_id = $3
	`, runtimeID, fcE2BScopeTypeChat, scopeID).Scan(&status); err != nil {
		t.Fatalf("load invalidated session: %v", err)
	}
	if status != "stale" {
		t.Fatalf("session status = %q, want stale", status)
	}
}

func TestResolveSandboxNeverReusesDifferentTemplate(t *testing.T) {
	pool := newSandboxLockPool(t)
	workspaceID, userID, runtimeID := seedFCE2BSandboxRuntime(t, pool, "Template Mismatch Sandbox")
	queries := db.New(pool)
	runtime, err := queries.GetAgentRuntime(context.Background(), runtimeID)
	if err != nil {
		t.Fatalf("load runtime: %v", err)
	}
	seedSession := func(scopeID pgtype.UUID, sandboxID string) {
		t.Helper()
		if _, err := queries.UpsertFCE2BSandboxSession(context.Background(), db.UpsertFCE2BSandboxSessionParams{
			WorkspaceID: workspaceID,
			RuntimeID:   runtimeID,
			ScopeType:   fcE2BScopeTypeChat,
			ScopeID:     scopeID,
			SandboxID:   sandboxID,
			Template:    "tpl_old",
			ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		}); err != nil {
			t.Fatalf("seed old sandbox session: %v", err)
		}
	}

	seedSession(workspaceID, "sbx_old")
	runner := &countingRunner{}
	launcher := NewFCE2BLauncher(queries, nil, FCE2BConfig{TimeoutSeconds: 300}, runner)
	launcher.SetPool(pool)
	sandboxID, coldStart, err := launcher.resolveSandbox(context.Background(), runtime, fcE2BTaskScope{typ: fcE2BScopeTypeChat, id: workspaceID}, true, "tpl_new", chattrace.New("task"))
	if err != nil {
		t.Fatalf("resolve new-template sandbox: %v", err)
	}
	if sandboxID == "sbx_old" || sandboxID != "sbx_1" || !coldStart {
		t.Fatalf("resolved sandbox = (%q, cold=%v), want a new cold sandbox", sandboxID, coldStart)
	}
	var template string
	if err := pool.QueryRow(context.Background(), `
		SELECT template FROM fc_e2b_sandbox_session
		WHERE runtime_id = $1 AND scope_type = $2 AND scope_id = $3
	`, runtimeID, fcE2BScopeTypeChat, workspaceID).Scan(&template); err != nil {
		t.Fatalf("load replacement session: %v", err)
	}
	if template != "tpl_new" {
		t.Fatalf("replacement template = %q, want tpl_new", template)
	}

	seedSession(userID, "sbx_old_failure")
	failing := NewFCE2BLauncher(queries, nil, FCE2BConfig{TimeoutSeconds: 300}, &countingRunner{createErr: errors.New("create failed")})
	failing.SetPool(pool)
	failedID, _, err := failing.resolveSandbox(context.Background(), runtime, fcE2BTaskScope{typ: fcE2BScopeTypeChat, id: userID}, true, "tpl_new", chattrace.New("task"))
	if err == nil {
		t.Fatal("new-template sandbox creation must fail")
	}
	if failedID != "" {
		t.Fatalf("failed rotation returned sandbox %q; old sandbox must not be reused", failedID)
	}
	var sandboxAfterFailure, templateAfterFailure string
	if err := pool.QueryRow(context.Background(), `
		SELECT sandbox_id, template FROM fc_e2b_sandbox_session
		WHERE runtime_id = $1 AND scope_type = $2 AND scope_id = $3
	`, runtimeID, fcE2BScopeTypeChat, userID).Scan(&sandboxAfterFailure, &templateAfterFailure); err != nil {
		t.Fatalf("load session after failed create: %v", err)
	}
	if sandboxAfterFailure != "sbx_old_failure" || templateAfterFailure != "tpl_old" {
		t.Fatalf("session after failed create = (%q, %q), want untouched old session", sandboxAfterFailure, templateAfterFailure)
	}
}
