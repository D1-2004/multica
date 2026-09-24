package userdecision

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPostgresDecisionTraceRetryAndConcurrentState(t *testing.T) {
	ctx, pool := preproductionTestDB(t)
	store := &Store{DB: pool, Environment: "integration-" + uuid.NewString()}
	poison, good := integrationRequest(store.Environment), integrationRequest(store.Environment)
	for _, r := range []Request{poison, good} {
		if _, err := store.Prepare(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, `DELETE FROM coordinator_user_decision WHERE environment=$1`, store.Environment)
	})
	mutated := false
	s := &Service{Pool: pool, Store: store, Observe: func(ctx context.Context, r Request) error {
		if r.ID == poison.ID {
			return errors.New("OTLP unavailable for this sample")
		}
		if !mutated {
			mutated = true
			// The callback must not wait on a projection-held row lock.
			_, err := pool.Exec(ctx, `UPDATE coordinator_user_decision SET state='send_unknown',updated_at=clock_timestamp() WHERE id=$1`, r.ID)
			return err
		}
		return nil
	}}
	if err := s.projectObservations(ctx); err == nil {
		t.Fatal("expected failed sample to remain retryable")
	}
	var poisonPending, goodPending bool
	if err := pool.QueryRow(ctx, `SELECT trace_exported_at IS NULL FROM coordinator_user_decision WHERE id=$1`, poison.ID).Scan(&poisonPending); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT trace_exported_at IS NOT NULL AND trace_exported_at<updated_at FROM coordinator_user_decision WHERE id=$1`, good.ID).Scan(&goodPending); err != nil {
		t.Fatal(err)
	}
	if !poisonPending || !goodPending {
		t.Fatal("failed or concurrent version acknowledged", poisonPending, goodPending)
	}
	// The failed sample is durably delayed; make it due to exercise recovery.
	if _, err := pool.Exec(ctx, `UPDATE coordinator_user_decision SET trace_next_attempt_at=now() WHERE id=$1`, poison.ID); err != nil {
		t.Fatal(err)
	}
	s.Observe = func(context.Context, Request) error { return nil }
	if err := s.projectObservations(ctx); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM coordinator_user_decision WHERE environment=$1 AND (trace_exported_at IS NULL OR trace_exported_at<updated_at)`, store.Environment).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatal(pending)
	}
}

func TestPostgresDecisionTracePartialProgressAfterCancellation(t *testing.T) {
	ctx, pool := preproductionTestDB(t)
	store := &Store{DB: pool, Environment: "integration-" + uuid.NewString()}
	first, second := integrationRequest(store.Environment), integrationRequest(store.Environment)
	for _, r := range []Request{first, second} {
		if _, err := store.Prepare(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM coordinator_user_decision WHERE environment=$1`, store.Environment)
	})
	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &Service{Pool: pool, Store: store, Observe: func(_ context.Context, r Request) error {
		if r.ID == second.ID {
			cancel()
			return context.Canceled
		}
		return nil
	}}
	if err := s.projectObservations(batchCtx); err == nil {
		t.Fatal("expected cancelled final export")
	}
	var exported, delayed bool
	if err := pool.QueryRow(ctx, `SELECT trace_exported_at=updated_at FROM coordinator_user_decision WHERE id=$1`, first.ID).Scan(&exported); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT trace_exported_at IS NULL AND trace_next_attempt_at>now() FROM coordinator_user_decision WHERE id=$1`, second.ID).Scan(&delayed); err != nil {
		t.Fatal(err)
	}
	if !exported || !delayed {
		t.Fatal("partial progress lost", exported, delayed)
	}
}

func TestPostgresDecisionTracePoisonBatchDoesNotStarveNewDecisions(t *testing.T) {
	ctx, pool := preproductionTestDB(t)
	store := &Store{DB: pool, Environment: "integration-" + uuid.NewString()}
	for i := 0; i < 21; i++ {
		if _, err := store.Prepare(ctx, integrationRequest(store.Environment)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM coordinator_user_decision WHERE environment=$1`, store.Environment)
	})
	seen := map[string]bool{}
	s := &Service{Pool: pool, Store: store, Observe: func(_ context.Context, r Request) error {
		if seen[r.ID] {
			t.Error("failed sample retried before backoff")
		}
		seen[r.ID] = true
		return errors.New("corrupt snapshot")
	}}
	_ = s.projectObservations(ctx)
	if len(seen) != 20 {
		t.Fatal(len(seen))
	}
	_ = s.projectObservations(ctx)
	if len(seen) != 21 {
		t.Fatal("poison batch starved newer sample", len(seen))
	}
}
