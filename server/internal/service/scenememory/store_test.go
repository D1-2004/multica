package scenememory

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func openPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database unreachable: %v", err)
	}
	var present bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables WHERE table_name = 'scene_memory'
	)`).Scan(&present); err != nil || !present {
		pool.Close()
		t.Skip("scene_memory table is not migrated")
	}
	var hasTriggerAt bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_name = 'scene_memory' AND column_name = 'pending_from_at'
	)`).Scan(&hasTriggerAt); err != nil || !hasTriggerAt {
		pool.Close()
		t.Skip("scene_memory pending_from_at is not migrated")
	}
	t.Cleanup(pool.Close)
	return pool
}

func testIdentity(t *testing.T) Identity {
	t.Helper()
	return Identity{
		WorkspaceID: util.MustParseUUID(uuid.NewString()),
		AgentID:     util.MustParseUUID(uuid.NewString()),
		Platform:    PlatformDingTalk,
		OrgID:       "org-" + uuid.NewString()[:8],
		SceneKey:    "cid" + uuid.NewString(),
		SceneKind:   KindGroup,
		SceneTitle:  "test scene",
	}
}

func seedAgentWrite(t *testing.T, pool *pgxpool.Pool, id Identity, enabled bool) {
	t.Helper()
	ctx := context.Background()
	var hasFlag bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_name = 'agent' AND column_name = 'scene_memory_write_enabled'
	)`).Scan(&hasFlag); err != nil || !hasFlag {
		t.Skip("agent scene_memory_write_enabled is not migrated")
	}
	slug := "sm" + strings.ReplaceAll(util.UUIDToString(id.WorkspaceID), "-", "")
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace (id, name, slug)
		VALUES ($1, $2, $2)
		ON CONFLICT (id) DO NOTHING
	`, id.WorkspaceID, slug); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent (
			id, workspace_id, name, description, runtime_mode, runtime_config,
			visibility, max_concurrent_tasks, scene_memory_write_enabled
		)
		VALUES ($1, $2, 'sm-test', '', 'local', '{}'::jsonb, 'workspace', 1, $3)
		ON CONFLICT (id) DO UPDATE SET
			scene_memory_write_enabled = EXCLUDED.scene_memory_write_enabled,
			archived_at = NULL
	`, id.AgentID, id.WorkspaceID, enabled); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM scene_memory WHERE workspace_id = $1`, id.WorkspaceID)
		_, _ = pool.Exec(ctx, `DELETE FROM agent WHERE id = $1`, id.AgentID)
		_, _ = pool.Exec(ctx, `DELETE FROM workspace WHERE id = $1`, id.WorkspaceID)
	})
}

func TestMarkDirtyIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	store := NewStore(db.New(pool))
	id := testIdentity(t)
	t.Cleanup(func() { _ = store.DeleteByWorkspace(ctx, id.WorkspaceID) })

	first, err := store.MarkDirty(ctx, id, DirtyTrigger{
		OccurredAt: time.Now().UTC(), EvidenceID: "m1", IdempotencyKey: "accept-1",
	})
	if err != nil {
		t.Fatalf("first dirty: %v", err)
	}
	second, err := store.MarkDirty(ctx, id, DirtyTrigger{
		OccurredAt: time.Now().UTC(), EvidenceID: "m2", IdempotencyKey: "accept-1",
	})
	if err != nil {
		t.Fatalf("repeat dirty: %v", err)
	}
	if first.DirtyRevision != 1 || second.DirtyRevision != 1 {
		t.Fatalf("dirty revision first=%d second=%d", first.DirtyRevision, second.DirtyRevision)
	}
	third, err := store.MarkDirty(ctx, id, DirtyTrigger{
		OccurredAt: time.Now().UTC().Add(time.Second), EvidenceID: "m3", IdempotencyKey: "accept-2",
	})
	if err != nil {
		t.Fatalf("new dirty: %v", err)
	}
	if third.DirtyRevision != 2 {
		t.Fatalf("dirty revision = %d, want 2", third.DirtyRevision)
	}
}

func TestEmptyInitializedDoesNotColdStartSignal(t *testing.T) {
	ctx := context.Background()
	store := NewStore(db.New(openPool(t)))
	id := testIdentity(t)
	t.Cleanup(func() { _ = store.DeleteByWorkspace(ctx, id.WorkspaceID) })
	row, err := store.Reset(ctx, id, DirtyTrigger{})
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if row.MemoryText != "" || !row.BootstrappedAt.Valid {
		t.Fatalf("reset should stamp bootstrapped empty text")
	}
}

func TestClaimTwoReplicas(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	store := NewStore(db.New(pool))
	id := testIdentity(t)
	seedAgentWrite(t, pool, id, true)
	t.Cleanup(func() { _ = store.DeleteByWorkspace(ctx, id.WorkspaceID) })
	if _, err := store.MarkDirty(ctx, id, DirtyTrigger{OccurredAt: time.Now().UTC(), EvidenceID: "m1", IdempotencyKey: "k1"}); err != nil {
		t.Fatalf("dirty: %v", err)
	}
	_, err := pool.Exec(ctx, `UPDATE scene_memory SET available_at = now() - interval '1 second' WHERE scene_key = $1`, id.SceneKey)
	if err != nil {
		t.Fatalf("nudge available_at: %v", err)
	}

	connA, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire a: %v", err)
	}
	defer connA.Release()
	connB, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire b: %v", err)
	}
	defer connB.Release()
	storeA := NewStore(db.New(connA.Conn()))
	storeB := NewStore(db.New(connB.Conn()))

	type result struct {
		row db.SceneMemory
		err error
	}
	out := make(chan result, 2)
	go func() {
		row, err := storeA.Claim(ctx)
		out <- result{row, err}
	}()
	go func() {
		row, err := storeB.Claim(ctx)
		out <- result{row, err}
	}()
	first := <-out
	second := <-out
	won := 0
	for _, item := range []result{first, second} {
		if item.err == nil {
			won++
			continue
		}
		if !errorsIsNoRows(item.err) {
			t.Fatalf("claim err = %v", item.err)
		}
	}
	if won != 1 {
		t.Fatalf("winners = %d, want 1", won)
	}
}

func TestCommitBatchDoesNotFalselyClean(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	store := NewStore(db.New(pool))
	row := claimReady(t, pool, store, testIdentity(t))
	cutoff := row.LeaseTargetThroughAt.Time
	mid := cutoff.Add(-time.Minute)
	updated, err := store.CommitBatch(ctx, row, CommitBatch{
		ReplaceText:            true,
		MemoryText:             "## 场域定位\n- x",
		SourceCursorAt:         mid,
		SourceCursorEvidenceID: "mid",
		ExpectedMemoryRevision: row.MemoryRevision,
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if updated.FlushedRevision != 0 {
		t.Fatalf("flushed_revision = %d after first batch", updated.FlushedRevision)
	}
	if err := store.FinishClaim(ctx, row); !errors.Is(err, ErrNotCaughtUp) {
		t.Fatalf("finish before cutoff: %v", err)
	}
}

func TestFinishClaimOnlyAfterCaughtUp(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	store := NewStore(db.New(pool))
	row := claimReady(t, pool, store, testIdentity(t))
	if _, err := store.CommitBatch(ctx, row, CommitBatch{
		SourceCursorAt:         row.LeaseTargetThroughAt.Time,
		SourceCursorEvidenceID: row.LeaseTargetThroughEvidenceID,
		ExpectedMemoryRevision: row.MemoryRevision,
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := store.FinishClaim(ctx, row); err != nil {
		t.Fatalf("finish: %v", err)
	}
	got, err := store.Get(ctx, testIdentityFromRow(row))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.FlushedRevision != got.DirtyRevision {
		t.Fatalf("flushed=%d dirty=%d", got.FlushedRevision, got.DirtyRevision)
	}
}

func TestNewDirtySurvivesOldFinish(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	store := NewStore(db.New(pool))
	id := testIdentity(t)
	row := claimReady(t, pool, store, id)
	if _, err := store.MarkDirty(ctx, id, DirtyTrigger{
		OccurredAt: time.Now().UTC().Add(time.Minute), EvidenceID: "later", IdempotencyKey: "k2",
	}); err != nil {
		t.Fatalf("second dirty: %v", err)
	}
	if _, err := store.CommitBatch(ctx, row, CommitBatch{
		SourceCursorAt:         row.LeaseTargetThroughAt.Time,
		SourceCursorEvidenceID: row.LeaseTargetThroughEvidenceID,
		ExpectedMemoryRevision: row.MemoryRevision,
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := store.FinishClaim(ctx, row); err != nil {
		t.Fatalf("finish: %v", err)
	}
	got, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.FlushedRevision != 1 || got.DirtyRevision != 2 {
		t.Fatalf("flushed=%d dirty=%d", got.FlushedRevision, got.DirtyRevision)
	}
	if StatusOf(got) != "pending" {
		t.Fatalf("status = %s, want pending", StatusOf(got))
	}
	if got.AttemptCount != 0 {
		t.Fatalf("finish with remainder kept attempt_count=%d", got.AttemptCount)
	}
}

func TestExpiredLeaseCannotWrite(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	store := NewStore(db.New(pool))
	row := claimReady(t, pool, store, testIdentity(t))
	if _, err := pool.Exec(ctx, `UPDATE scene_memory SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, row.ID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	if _, err := store.CommitBatch(ctx, row, CommitBatch{
		SourceCursorAt:         time.Now().UTC(),
		ExpectedMemoryRevision: row.MemoryRevision,
	}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("commit expired: %v", err)
	}
	if err := store.FinishClaim(ctx, row); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("finish expired: %v", err)
	}
	if err := store.Retry(ctx, row, time.Second, "", "expired"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("retry expired: %v", err)
	}
}

func TestBlockedCanBeAwakened(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	store := NewStore(db.New(pool))
	id := testIdentity(t)
	row := claimReady(t, pool, store, id)
	if err := store.Block(ctx, row, ErrorAuth, "binding gone"); err != nil {
		t.Fatalf("block: %v", err)
	}
	got, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if StatusOf(got) != "blocked" {
		t.Fatalf("status = %s", StatusOf(got))
	}
	woke, err := store.MarkDirty(ctx, id, DirtyTrigger{
		OccurredAt: time.Now().UTC().Add(time.Minute), EvidenceID: "wake", IdempotencyKey: "wake",
	})
	if err != nil {
		t.Fatalf("wake: %v", err)
	}
	if woke.BlockedAt.Valid {
		t.Fatal("new dirty should clear blocked")
	}
}

func TestGetIsolatesSceneKeys(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	store := NewStore(db.New(pool))
	a := testIdentity(t)
	a.SceneKind = KindGroup
	b := a
	b.SceneKey = "cid-b-" + uuid.NewString()
	b.SceneKind = KindGroup
	seedAgentWrite(t, pool, a, true)
	t.Cleanup(func() { _ = store.DeleteByWorkspace(ctx, a.WorkspaceID) })

	commitSceneText(t, pool, store, a, "GAMMA-A-881 是报表工具")
	commitSceneText(t, pool, store, b, "这个群还没有口径")

	gotA, err := store.Get(ctx, a)
	if err != nil {
		t.Fatalf("get A: %v", err)
	}
	gotB, err := store.Get(ctx, b)
	if err != nil {
		t.Fatalf("get B: %v", err)
	}
	if !strings.Contains(gotA.MemoryText, "报表工具") || strings.Contains(gotA.MemoryText, "还没有口径") {
		t.Fatalf("A=%q", gotA.MemoryText)
	}
	if !strings.Contains(gotB.MemoryText, "还没有口径") || strings.Contains(gotB.MemoryText, "报表工具") {
		t.Fatalf("B=%q", gotB.MemoryText)
	}

	reset, err := store.Reset(ctx, a, DirtyTrigger{})
	if err != nil {
		t.Fatalf("reset A: %v", err)
	}
	if reset.MemoryText != "" {
		t.Fatalf("A after reset=%q", reset.MemoryText)
	}
	kept, err := store.Get(ctx, b)
	if err != nil {
		t.Fatalf("get B after reset: %v", err)
	}
	if !strings.Contains(kept.MemoryText, "还没有口径") {
		t.Fatalf("reset A cleared B: %q", kept.MemoryText)
	}
}

func commitSceneText(t *testing.T, pool *pgxpool.Pool, store *Store, id Identity, text string) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.MarkDirty(ctx, id, DirtyTrigger{
		OccurredAt: time.Now().UTC(), EvidenceID: "m-" + id.SceneKey, IdempotencyKey: "k-" + id.SceneKey,
	}); err != nil {
		t.Fatalf("dirty %s: %v", id.SceneKey, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE scene_memory SET available_at = now() - interval '1 second' WHERE scene_key = $1`, id.SceneKey); err != nil {
		t.Fatalf("nudge %s: %v", id.SceneKey, err)
	}
	row, err := store.Claim(ctx)
	if err != nil {
		t.Fatalf("claim %s: %v", id.SceneKey, err)
	}
	if row.SceneKey != id.SceneKey {
		t.Fatalf("claimed %s, want %s", row.SceneKey, id.SceneKey)
	}
	if _, err := store.CommitBatch(ctx, row, CommitBatch{
		ReplaceText:            true,
		MemoryText:             text,
		SourceCursorAt:         row.LeaseTargetThroughAt.Time,
		SourceCursorEvidenceID: row.LeaseTargetThroughEvidenceID,
		ExpectedMemoryRevision: row.MemoryRevision,
	}); err != nil {
		t.Fatalf("commit %s: %v", id.SceneKey, err)
	}
	if err := store.FinishClaim(ctx, row); err != nil {
		t.Fatalf("finish %s: %v", id.SceneKey, err)
	}
}

func TestResetClearsTextKeepsBootstrap(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	store := NewStore(db.New(pool))
	id := testIdentity(t)
	other := testIdentity(t)
	other.WorkspaceID = id.WorkspaceID
	t.Cleanup(func() { _ = store.DeleteByWorkspace(ctx, id.WorkspaceID) })
	if _, err := store.MarkDirty(ctx, other, DirtyTrigger{OccurredAt: time.Now().UTC(), EvidenceID: "o1", IdempotencyKey: "o1"}); err != nil {
		t.Fatalf("other dirty: %v", err)
	}
	row := claimReady(t, pool, store, id)
	if _, err := store.CommitBatch(ctx, row, CommitBatch{
		ReplaceText:            true,
		MemoryText:             "old fact",
		SourceCursorAt:         row.LeaseTargetThroughAt.Time,
		SourceCursorEvidenceID: row.LeaseTargetThroughEvidenceID,
		ExpectedMemoryRevision: row.MemoryRevision,
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := store.FinishClaim(ctx, row); err != nil {
		t.Fatalf("finish: %v", err)
	}
	reset, err := store.Reset(ctx, id, DirtyTrigger{})
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if reset.MemoryText != "" || !reset.BootstrappedAt.Valid || reset.DirtyRevision != reset.FlushedRevision {
		t.Fatalf("reset row = %+v", reset)
	}
	if reset.LastTriggerIdempotencyKey != "" || reset.DirtyThroughAt.Valid ||
		reset.LastTriggerAt.Valid || reset.LastTriggerEvidenceID != "" {
		t.Fatalf("reset left dirty trigger state: %+v", reset)
	}
	if reset.MemoryRevision <= row.MemoryRevision {
		t.Fatalf("reset memory_revision=%d, before=%d", reset.MemoryRevision, row.MemoryRevision)
	}
	replay, err := store.MarkDirty(ctx, id, DirtyTrigger{
		OccurredAt: time.Now().UTC(), EvidenceID: "m1", IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatalf("dirty after reset: %v", err)
	}
	if replay.DirtyRevision <= reset.DirtyRevision {
		t.Fatalf("reset must allow the previous idempotency key, dirty=%d", replay.DirtyRevision)
	}
	kept, err := store.Get(ctx, other)
	if err != nil {
		t.Fatalf("other get: %v", err)
	}
	if kept.DirtyRevision == 0 {
		t.Fatal("reset must not touch another scene")
	}
}

func TestResetMissingRowSucceeds(t *testing.T) {
	ctx := context.Background()
	store := NewStore(db.New(openPool(t)))
	id := testIdentity(t)
	t.Cleanup(func() { _ = store.DeleteByWorkspace(ctx, id.WorkspaceID) })
	row, err := store.Reset(ctx, id, DirtyTrigger{})
	if err != nil {
		t.Fatalf("reset missing: %v", err)
	}
	if !row.BootstrappedAt.Valid || row.MemoryText != "" {
		t.Fatalf("inserted reset = %+v", row)
	}
}

func TestWorkspaceCleanup(t *testing.T) {
	ctx := context.Background()
	store := NewStore(db.New(openPool(t)))
	id := testIdentity(t)
	if _, err := store.Reset(ctx, id, DirtyTrigger{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.DeleteByWorkspace(ctx, id.WorkspaceID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Get(ctx, id); !errorsIsNoRows(err) {
		t.Fatalf("get after delete: %v", err)
	}
}

func dirtyReady(t *testing.T, pool *pgxpool.Pool, store *Store, id Identity) {
	t.Helper()
	ctx := context.Background()
	seedAgentWrite(t, pool, id, true)
	t.Cleanup(func() { _ = store.DeleteByWorkspace(ctx, id.WorkspaceID) })
	if _, err := store.MarkDirty(ctx, id, DirtyTrigger{
		OccurredAt: time.Now().UTC(), EvidenceID: "m1", IdempotencyKey: "k1",
	}); err != nil {
		t.Fatalf("dirty: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE scene_memory SET available_at = now() - interval '1 second' WHERE scene_key = $1`, id.SceneKey); err != nil {
		t.Fatalf("nudge: %v", err)
	}
}

func claimReady(t *testing.T, pool *pgxpool.Pool, store *Store, id Identity) db.SceneMemory {
	t.Helper()
	dirtyReady(t, pool, store, id)
	row, err := store.Claim(context.Background())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	return row
}

func testIdentityFromRow(row db.SceneMemory) Identity {
	return Identity{
		WorkspaceID: row.WorkspaceID,
		AgentID:     row.AgentID,
		Platform:    row.Platform,
		OrgID:       row.OrgID,
		SceneKey:    row.SceneKey,
		SceneKind:   row.SceneKind,
	}
}

func errorsIsNoRows(err error) bool {
	return err != nil && (errors.Is(err, pgx.ErrNoRows) || err.Error() == pgx.ErrNoRows.Error())
}

func TestClaimRequiresWriteEnabled(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	store := NewStore(db.New(pool))
	id := testIdentity(t)
	seedAgentWrite(t, pool, id, false)
	if _, err := store.MarkDirty(ctx, id, DirtyTrigger{OccurredAt: time.Now().UTC(), EvidenceID: "m1", IdempotencyKey: "k1"}); err != nil {
		t.Fatalf("dirty: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE scene_memory SET available_at = now() - interval '1 second' WHERE scene_key = $1`, id.SceneKey); err != nil {
		t.Fatalf("nudge: %v", err)
	}
	if _, err := store.Claim(ctx); !errorsIsNoRows(err) {
		t.Fatalf("claim with write disabled: %v", err)
	}
}

func TestBlankIdempotencyKeyAlwaysDirties(t *testing.T) {
	ctx := context.Background()
	store := NewStore(db.New(openPool(t)))
	id := testIdentity(t)
	t.Cleanup(func() { _ = store.DeleteByWorkspace(ctx, id.WorkspaceID) })
	first, err := store.MarkDirty(ctx, id, DirtyTrigger{OccurredAt: time.Now().UTC(), EvidenceID: "a", IdempotencyKey: ""})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := store.MarkDirty(ctx, id, DirtyTrigger{OccurredAt: time.Now().UTC().Add(time.Second), EvidenceID: "b", IdempotencyKey: ""})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second.DirtyRevision <= first.DirtyRevision {
		t.Fatalf("blank idempotency must dirty again, first=%d second=%d", first.DirtyRevision, second.DirtyRevision)
	}
}

func TestExpiredLeaseIsExcludedFromFenceCount(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	store := NewStore(db.New(pool))
	row := claimReady(t, pool, store, testIdentity(t))
	before, err := store.CountValidLeases(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if before < 1 {
		t.Fatalf("valid leases = %d", before)
	}
	if _, err := pool.Exec(ctx, `UPDATE scene_memory SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, row.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}
	after, err := store.CountValidLeases(ctx)
	if err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after >= before {
		t.Fatalf("expired lease still counted: before=%d after=%d", before, after)
	}
}

func TestDeleteByAgent(t *testing.T) {
	ctx := context.Background()
	store := NewStore(db.New(openPool(t)))
	id := testIdentity(t)
	kept := testIdentity(t)
	kept.WorkspaceID = id.WorkspaceID
	t.Cleanup(func() { _ = store.DeleteByWorkspace(ctx, id.WorkspaceID) })
	if _, err := store.Reset(ctx, id, DirtyTrigger{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := store.Reset(ctx, kept, DirtyTrigger{}); err != nil {
		t.Fatalf("seed kept: %v", err)
	}
	if err := store.DeleteByAgent(ctx, id.WorkspaceID, id.AgentID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Get(ctx, id); !errorsIsNoRows(err) {
		t.Fatalf("get deleted: %v", err)
	}
	if _, err := store.Get(ctx, kept); err != nil {
		t.Fatalf("kept scene: %v", err)
	}
}

func TestReleasePendingDropsLeaseWithoutRetry(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	store := NewStore(db.New(pool))
	row := claimReady(t, pool, store, testIdentity(t))
	if err := store.ReleasePending(ctx, row); err != nil {
		t.Fatalf("release: %v", err)
	}
	got, err := store.Get(ctx, testIdentityFromRow(row))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.LeaseToken.Valid || StatusOf(got) != "pending" || got.LastError != "" {
		t.Fatalf("released row = %+v status=%s", got, StatusOf(got))
	}
}

func TestResetKeepsNewerTrigger(t *testing.T) {
	ctx := context.Background()
	store := NewStore(db.New(openPool(t)))
	id := testIdentity(t)
	t.Cleanup(func() { _ = store.DeleteByWorkspace(ctx, id.WorkspaceID) })
	resetAt := time.Now().UTC().Truncate(time.Microsecond).Add(-2 * time.Second)
	later := resetAt.Add(3 * time.Second)
	if _, err := store.MarkDirty(ctx, id, DirtyTrigger{
		OccurredAt: later, EvidenceID: "msg-later", IdempotencyKey: "k-later",
	}); err != nil {
		t.Fatalf("later dirty: %v", err)
	}
	row, err := store.Reset(ctx, id, DirtyTrigger{OccurredAt: resetAt, EvidenceID: "msg-reset"})
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if row.MemoryText != "" {
		t.Fatalf("reset must clear text, got %q", row.MemoryText)
	}
	if row.LastTriggerEvidenceID != "msg-later" {
		t.Fatalf("newer trigger must survive reset, got %q", row.LastTriggerEvidenceID)
	}
	if row.DirtyRevision <= row.FlushedRevision {
		t.Fatalf("newer dirty must stay pending, dirty=%d flushed=%d", row.DirtyRevision, row.FlushedRevision)
	}
	if row.SourceCursorEvidenceID != "msg-reset" {
		t.Fatalf("cursor must be the reset cutoff, got %q", row.SourceCursorEvidenceID)
	}
}

func TestResetClearsOlderTrigger(t *testing.T) {
	ctx := context.Background()
	store := NewStore(db.New(openPool(t)))
	id := testIdentity(t)
	t.Cleanup(func() { _ = store.DeleteByWorkspace(ctx, id.WorkspaceID) })
	older := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	resetAt := older.Add(30 * time.Second)
	if _, err := store.MarkDirty(ctx, id, DirtyTrigger{
		OccurredAt: older, EvidenceID: "msg-old", IdempotencyKey: "k-old",
	}); err != nil {
		t.Fatalf("old dirty: %v", err)
	}
	row, err := store.Reset(ctx, id, DirtyTrigger{OccurredAt: resetAt, EvidenceID: "msg-reset"})
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if row.LastTriggerAt.Valid || row.LastTriggerEvidenceID != "" || row.DirtyThroughAt.Valid {
		t.Fatalf("older trigger must clear, got %+v", row)
	}
	if row.DirtyRevision != row.FlushedRevision {
		t.Fatalf("older dirty must catch up, dirty=%d flushed=%d", row.DirtyRevision, row.FlushedRevision)
	}
}

func TestMarkDirtyKeepsHighWaterAndCurrentTrigger(t *testing.T) {
	ctx := context.Background()
	store := NewStore(db.New(openPool(t)))
	id := testIdentity(t)
	t.Cleanup(func() { _ = store.DeleteByWorkspace(ctx, id.WorkspaceID) })
	later := time.Now().UTC().Truncate(time.Microsecond)
	earlier := later.Add(-time.Second)
	first, err := store.MarkDirty(ctx, id, DirtyTrigger{
		OccurredAt: later, EvidenceID: "msg-later", IdempotencyKey: "k-later",
	})
	if err != nil {
		t.Fatalf("later dirty: %v", err)
	}
	second, err := store.MarkDirty(ctx, id, DirtyTrigger{
		OccurredAt: earlier, EvidenceID: "msg-early", IdempotencyKey: "k-early",
	})
	if err != nil {
		t.Fatalf("earlier dirty: %v", err)
	}
	if second.DirtyRevision <= first.DirtyRevision {
		t.Fatalf("earlier trigger must still dirty, first=%d second=%d", first.DirtyRevision, second.DirtyRevision)
	}
	if second.DirtyThroughEvidenceID != "msg-later" {
		t.Fatalf("high-water must stay later, dirty_through=%q", second.DirtyThroughEvidenceID)
	}
	if second.LastTriggerEvidenceID != "msg-early" {
		t.Fatalf("last_trigger must be the current inbound, got %q", second.LastTriggerEvidenceID)
	}
	if !second.LastTriggerAt.Valid || second.LastTriggerAt.Time.UTC().Unix() != earlier.Unix() {
		t.Fatalf("last_trigger_at=%v want %s", second.LastTriggerAt, earlier)
	}
}

func TestMarkDirtyThenPlanFlushIncludesEarlierTrigger(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	store := NewStore(db.New(pool))
	id := testIdentity(t)
	later := time.Now().UTC().Truncate(time.Microsecond)
	earlier := later.Add(-time.Second)
	seedAgentWrite(t, pool, id, true)
	if _, err := store.MarkDirty(ctx, id, DirtyTrigger{
		OccurredAt: later, EvidenceID: "msg-later", IdempotencyKey: "k-later",
	}); err != nil {
		t.Fatalf("later dirty: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE scene_memory SET available_at = now() - interval '1 second' WHERE scene_key = $1`, id.SceneKey); err != nil {
		t.Fatalf("nudge later: %v", err)
	}
	row, err := store.Claim(ctx)
	if err != nil {
		t.Fatalf("claim later: %v", err)
	}
	if _, err := store.CommitBatch(ctx, row, CommitBatch{
		SourceCursorAt:         later,
		SourceCursorEvidenceID: "msg-later",
		ExpectedMemoryRevision: row.MemoryRevision,
	}); err != nil {
		t.Fatalf("commit later: %v", err)
	}
	if err := store.FinishClaim(ctx, row); err != nil {
		t.Fatalf("finish later: %v", err)
	}
	if _, err := store.MarkDirty(ctx, id, DirtyTrigger{
		OccurredAt: earlier, EvidenceID: "msg-early", IdempotencyKey: "k-early",
	}); err != nil {
		t.Fatalf("earlier dirty: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE scene_memory SET available_at = now() - interval '1 second' WHERE scene_key = $1`, id.SceneKey); err != nil {
		t.Fatalf("nudge early: %v", err)
	}
	row, err = store.Claim(ctx)
	if err != nil {
		t.Fatalf("claim early: %v", err)
	}
	if row.LeaseTargetThroughEvidenceID != "msg-later" {
		t.Fatalf("lease cutoff must stay the high-water, got %q", row.LeaseTargetThroughEvidenceID)
	}
	if row.LastTriggerEvidenceID != "msg-early" {
		t.Fatalf("claimed last_trigger must be the current inbound, got %q", row.LastTriggerEvidenceID)
	}
	if row.SourceCursorEvidenceID != "msg-later" {
		t.Fatalf("cursor must still be the flushed high-water, got %q", row.SourceCursorEvidenceID)
	}
	plan, err := planFlush(row, []HistoryEvent{
		{EvidenceID: "msg-early", OccurredAt: earlier, Content: "纠正：DELTA-5520 是排班表"},
		{EvidenceID: "msg-later", OccurredAt: later, Content: "灌水"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsEvidence(plan.batch, "msg-early") {
		t.Fatalf("MarkDirty→planFlush must merge the earlier trigger: %+v", plan)
	}
	if !plan.caughtUp {
		t.Fatalf("visible trigger plus covered high-water must catch up: %+v", plan)
	}
}

func TestMarkDirtySameSecondSmallerEvidence(t *testing.T) {
	ctx := context.Background()
	store := NewStore(db.New(openPool(t)))
	id := testIdentity(t)
	t.Cleanup(func() { _ = store.DeleteByWorkspace(ctx, id.WorkspaceID) })
	at := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := store.MarkDirty(ctx, id, DirtyTrigger{
		OccurredAt: at, EvidenceID: "zzz", IdempotencyKey: "k-zzz",
	}); err != nil {
		t.Fatalf("later id: %v", err)
	}
	row, err := store.MarkDirty(ctx, id, DirtyTrigger{
		OccurredAt: at, EvidenceID: "aaa", IdempotencyKey: "k-aaa",
	})
	if err != nil {
		t.Fatalf("smaller id: %v", err)
	}
	if row.DirtyThroughEvidenceID != "zzz" {
		t.Fatalf("high-water must stay zzz, got %q", row.DirtyThroughEvidenceID)
	}
	if row.LastTriggerEvidenceID != "aaa" {
		t.Fatalf("last_trigger must be aaa, got %q", row.LastTriggerEvidenceID)
	}
}
