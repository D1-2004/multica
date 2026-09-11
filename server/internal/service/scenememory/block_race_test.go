package scenememory

import (
	"context"
	"errors"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A trigger that arrives while a flush is still running clears blocked_at
// and bumps dirty_revision. The stale claim's Block must then affect no row
// (ErrLeaseLost) instead of re-blocking the scene the trigger just woke.
func TestBlockSkipsClaimSupersededByNewTrigger(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	store := NewStore(db.New(pool))
	id := testIdentity(t)
	row := claimReady(t, pool, store, id)
	if _, err := store.MarkDirty(ctx, id, DirtyTrigger{
		OccurredAt: time.Now().UTC().Add(time.Minute), EvidenceID: "rejoined", IdempotencyKey: "rejoined",
	}); err != nil {
		t.Fatalf("trigger during flush: %v", err)
	}
	if err := store.Block(ctx, row, ErrorNotInConversation, "130003"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("block on a superseded claim must be a no-op, got %v", err)
	}
	got, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.BlockedAt.Valid || got.LastErrorCode == ErrorNotInConversation {
		t.Fatalf("stale claim re-blocked the scene: status=%s code=%q", StatusOf(got), got.LastErrorCode)
	}
}

func TestBlockStillWorksWithoutNewTrigger(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	store := NewStore(db.New(pool))
	id := testIdentity(t)
	row := claimReady(t, pool, store, id)
	if err := store.Block(ctx, row, ErrorNotInConversation, "130003"); err != nil {
		t.Fatalf("block: %v", err)
	}
	got, err := store.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if StatusOf(got) != "blocked" || got.LastErrorCode != ErrorNotInConversation {
		t.Fatalf("status=%s code=%q", StatusOf(got), got.LastErrorCode)
	}
}
