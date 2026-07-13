package service

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// countingRunner records every `sandbox create` and hands back a distinct id,
// so a duplicate boot is visible rather than silently idempotent.
type countingRunner struct {
	mu      sync.Mutex
	creates int
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
		r.mu.Unlock()
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
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
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
			id, _, err := l.resolveSandbox(ctx, runtime, scope, true, "tpl_test")
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
