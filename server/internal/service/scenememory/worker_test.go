package scenememory

import (
	"context"
	"errors"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type stubFlusher struct {
	err error
}

func (s stubFlusher) Flush(context.Context, Memory) error {
	return s.err
}

func TestProcessNextNilFlusherDoesNotClaim(t *testing.T) {
	w := NewWorker(&Store{}, nil, nil)
	worked, err := w.ProcessNext(context.Background())
	if worked || err != nil {
		t.Fatalf("nil flusher worked=%v err=%v", worked, err)
	}
}

func TestProcessNextDoesNotRetryCanceled(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	store := NewStore(db.New(pool))
	id := testIdentity(t)
	dirtyReady(t, pool, store, id)
	w := NewWorker(store, stubFlusher{err: context.Canceled}, nil)
	worked, err := w.ProcessNext(ctx)
	if !worked || !errors.Is(err, context.Canceled) {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	got, getErr := store.Get(ctx, id)
	if getErr != nil {
		t.Fatalf("get: %v", getErr)
	}
	if StatusOf(got) == "retrying" {
		t.Fatal("canceled flush must not Retry")
	}
}

func TestProcessNextBlocksAuth(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	store := NewStore(db.New(pool))
	id := testIdentity(t)
	dirtyReady(t, pool, store, id)
	w := NewWorker(store, stubFlusher{err: &FlushError{Code: ErrorAuth, Err: errors.New("binding gone")}}, nil)
	worked, err := w.ProcessNext(ctx)
	if !worked || err == nil {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	got, getErr := store.Get(ctx, id)
	if getErr != nil {
		t.Fatalf("get: %v", getErr)
	}
	if StatusOf(got) != "blocked" {
		t.Fatalf("status = %s, want blocked", StatusOf(got))
	}
}

func TestProcessNextRetriesPlainError(t *testing.T) {
	ctx := context.Background()
	pool := openPool(t)
	store := NewStore(db.New(pool))
	id := testIdentity(t)
	dirtyReady(t, pool, store, id)
	w := NewWorker(store, stubFlusher{err: errors.New("dws timeout")}, nil)
	worked, err := w.ProcessNext(ctx)
	if !worked || err == nil {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	got, getErr := store.Get(ctx, id)
	if getErr != nil {
		t.Fatalf("get: %v", getErr)
	}
	if StatusOf(got) != "retrying" {
		t.Fatalf("status = %s, want retrying", StatusOf(got))
	}
	if !got.AvailableAt.Time.After(time.Now().UTC().Add(3 * time.Second)) {
		t.Fatalf("retry available_at = %s", got.AvailableAt.Time)
	}
}
