package dingtalk

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestMemSessionStoreFencesOlderRegistrationGeneration(t *testing.T) {
	store := newMemSessionStore()
	workspaceID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	agentID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	first, err := store.Create(context.Background(), sessionRecord{
		ID: "stream", WorkspaceID: workspaceID, TransportMode: TransportModeStream,
	}, agentID)
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := store.Create(context.Background(), sessionRecord{
		ID: "http", WorkspaceID: workspaceID, TransportMode: TransportModeHTTPCallback,
	}, agentID)
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	if first != 1 || second != 2 {
		t.Fatalf("generations = %d, %d", first, second)
	}
	current, err := store.IsCurrent(context.Background(), workspaceID, agentID, first)
	if err != nil || current {
		t.Fatalf("old generation current=%v err=%v", current, err)
	}
	current, err = store.IsCurrent(context.Background(), workspaceID, agentID, second)
	if err != nil || !current {
		t.Fatalf("new generation current=%v err=%v", current, err)
	}
}

// newStoreTestFixture connects to the test database and seeds the workspace +
// agent rows the session FKs require. Each store instance stands in for one
// replica: the bug this file guards against is that a session opened by one
// replica was invisible to every other one.
func newStoreTestFixture(t *testing.T) (*pgxpool.Pool, pgtype.UUID, pgtype.UUID, pgtype.UUID) {
	t.Helper()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("database not available: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database not available: %v", err)
	}
	t.Cleanup(pool.Close)

	var workspaceID, userID, agentID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('DingTalk Install Session Tests', $1, '', 'DIS')
		RETURNING id
	`, "dt-install-session-"+randomSuffix(t)).Scan(&workspaceID); err != nil {
		t.Skipf("seed workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
	})

	if err := pool.QueryRow(ctx, `
		INSERT INTO "user" (email, name) VALUES ($1, 'Install Session Tester')
		RETURNING id
	`, "install-session-"+randomSuffix(t)+"@example.com").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID)
	})

	var runtimeID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, last_seen_at, visibility, owner_id
		)
		VALUES ($1, NULL, 'Install Session Runtime', 'cloud', 'test', 'online',
			'', '{}'::jsonb, now(), 'private', $2)
		RETURNING id
	`, workspaceID, userID).Scan(&runtimeID); err != nil {
		t.Fatalf("seed runtime: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, permission_mode, max_concurrent_tasks, owner_id,
			instructions, custom_env, custom_args, mcp_config
		)
		VALUES ($1, 'Install Session Agent', '', 'cloud', '{}'::jsonb,
			$2, 'workspace', 'public_to', 1, $3, '', '{}'::jsonb, '[]'::jsonb, '{}'::jsonb)
		RETURNING id
	`, workspaceID, runtimeID, userID).Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	return pool, workspaceID, agentID, userID
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	id, err := randomSessionID()
	if err != nil {
		t.Fatalf("randomSessionID: %v", err)
	}
	return id[:8]
}

// The regression: /install/begin is served by one replica and the browser's
// status polls are load-balanced across all of them. With the session in a
// process-local map, every poll that landed elsewhere returned
// "install session not found" seconds after the QR appeared.
func TestDBSessionStoreIsVisibleAcrossReplicas(t *testing.T) {
	pool, workspaceID, agentID, userID := newStoreTestFixture(t)
	ctx := context.Background()

	// Two stores over the same database = two replicas.
	replicaA := &dbSessionStore{q: db.New(pool)}
	replicaB := &dbSessionStore{q: db.New(pool)}

	sessionID, err := randomSessionID()
	if err != nil {
		t.Fatalf("randomSessionID: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM dingtalk_install_session WHERE id = $1`, sessionID)
	})

	if _, err := replicaA.Create(ctx, sessionRecord{
		ID:            sessionID,
		WorkspaceID:   workspaceID,
		Status:        RegistrationStatusPending,
		ExpiresAt:     time.Now().Add(10 * time.Minute),
		TransportMode: TransportModeStream,
	}, agentID); err != nil {
		t.Fatalf("replica A create: %v", err)
	}

	rec, err := replicaB.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("replica B must see the session replica A opened: %v", err)
	}
	if rec.Status != RegistrationStatusPending {
		t.Errorf("status = %q, want pending", rec.Status)
	}
	if rec.WorkspaceID != workspaceID {
		t.Errorf("workspace mismatch: got %v want %v", rec.WorkspaceID, workspaceID)
	}

	// The polling goroutine (replica A) records the outcome; the browser may
	// well be polling replica B when it does.
	var installationID pgtype.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO channel_installation (
			workspace_id, agent_id, channel_type, config, status, installer_user_id
		)
		VALUES ($1, $2, 'dingtalk', '{}'::jsonb, 'active', $3)
		RETURNING id
	`, workspaceID, agentID, userID).Scan(&installationID); err != nil {
		t.Fatalf("seed installation: %v", err)
	}
	if err := replicaA.FinishSuccess(ctx, sessionID, installationID, time.Now().Add(30*time.Minute)); err != nil {
		t.Fatalf("replica A finish: %v", err)
	}

	rec, err = replicaB.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("replica B get after finish: %v", err)
	}
	if rec.Status != RegistrationStatusSuccess {
		t.Errorf("status = %q, want success", rec.Status)
	}
	if rec.InstallationID != installationID {
		t.Errorf("installation id = %v, want %v", rec.InstallationID, installationID)
	}
}

func TestDBSessionStoreTerminalIsWriteOnce(t *testing.T) {
	pool, workspaceID, agentID, _ := newStoreTestFixture(t)
	ctx := context.Background()
	store := &dbSessionStore{q: db.New(pool)}

	sessionID, _ := randomSessionID()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM dingtalk_install_session WHERE id = $1`, sessionID)
	})
	if _, err := store.Create(ctx, sessionRecord{
		ID: sessionID, WorkspaceID: workspaceID, Status: RegistrationStatusPending,
		ExpiresAt: time.Now().Add(10 * time.Minute), TransportMode: TransportModeStream,
	}, agentID); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := store.FinishError(ctx, sessionID, RegistrationReasonProtocol, "boom", time.Now().Add(30*time.Minute)); err != nil {
		t.Fatalf("finish error: %v", err)
	}
	// A late success must not overwrite a recorded failure.
	if err := store.FinishSuccess(ctx, sessionID, pgtype.UUID{}, time.Now().Add(30*time.Minute)); err != nil {
		t.Fatalf("late finish success: %v", err)
	}

	rec, err := store.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if rec.Status != RegistrationStatusError || rec.ErrorReason != RegistrationReasonProtocol {
		t.Errorf("terminal state overwritten: got %q/%q", rec.Status, rec.ErrorReason)
	}
}

func TestDBSessionStoreSweep(t *testing.T) {
	pool, workspaceID, agentID, _ := newStoreTestFixture(t)
	ctx := context.Background()
	store := &dbSessionStore{q: db.New(pool)}
	now := time.Now()

	staleID, _ := randomSessionID()
	liveID, _ := randomSessionID()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM dingtalk_install_session WHERE id = ANY($1)`, []string{staleID, liveID})
	})

	for _, id := range []string{staleID, liveID} {
		if _, err := store.Create(ctx, sessionRecord{
			ID: id, WorkspaceID: workspaceID, Status: RegistrationStatusPending,
			ExpiresAt: now.Add(10 * time.Minute), TransportMode: TransportModeStream,
		}, agentID); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	// stale: terminal and past its GC window. live: terminal but still inside it.
	if err := store.FinishError(ctx, staleID, RegistrationReasonExpired, "gone", now.Add(-time.Minute)); err != nil {
		t.Fatalf("finish stale: %v", err)
	}
	if err := store.FinishSuccess(ctx, liveID, pgtype.UUID{}, now.Add(30*time.Minute)); err != nil {
		t.Fatalf("finish live: %v", err)
	}

	if err := store.Sweep(ctx, now); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if _, err := store.Get(ctx, staleID); !errors.Is(err, ErrRegistrationSessionNotFound) {
		t.Errorf("stale session should be swept, got err=%v", err)
	}
	if _, err := store.Get(ctx, liveID); err != nil {
		t.Errorf("live session must survive the sweep: %v", err)
	}
}

func TestDBSessionStoreFencesOlderRegistrationGeneration(t *testing.T) {
	pool, workspaceID, agentID, _ := newStoreTestFixture(t)
	ctx := context.Background()
	store := &dbSessionStore{q: db.New(pool)}

	streamID, _ := randomSessionID()
	httpID, _ := randomSessionID()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM dingtalk_install_session WHERE id = ANY($1)`, []string{streamID, httpID})
	})

	first, err := store.Create(ctx, sessionRecord{
		ID: streamID, WorkspaceID: workspaceID, Status: RegistrationStatusPending,
		ExpiresAt: time.Now().Add(10 * time.Minute), TransportMode: TransportModeStream,
	}, agentID)
	if err != nil {
		t.Fatalf("create Stream generation: %v", err)
	}
	second, err := store.Create(ctx, sessionRecord{
		ID: httpID, WorkspaceID: workspaceID, Status: RegistrationStatusPending,
		ExpiresAt: time.Now().Add(10 * time.Minute), TransportMode: TransportModeHTTPCallback,
	}, agentID)
	if err != nil {
		t.Fatalf("create HTTP callback generation: %v", err)
	}
	if first != 1 || second != 2 {
		t.Fatalf("generations = %d, %d; want 1, 2", first, second)
	}
	if current, err := store.IsCurrent(ctx, workspaceID, agentID, first); err != nil || current {
		t.Fatalf("older generation current=%v err=%v", current, err)
	}
	if current, err := store.IsCurrent(ctx, workspaceID, agentID, second); err != nil || !current {
		t.Fatalf("latest generation current=%v err=%v", current, err)
	}
}
