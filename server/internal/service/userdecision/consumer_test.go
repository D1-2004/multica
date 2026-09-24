package userdecision

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestPersistCardEventRetriesAmbiguousCommit(t *testing.T) {
	calls := 0
	outcome, err := persistCardEvent(context.Background(), func(context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("connection lost after commit")
		}
		return "duplicate_event", nil
	}, time.Microsecond)
	if err != nil || outcome != "duplicate_event" || calls != 2 {
		t.Fatal(outcome, err, calls)
	}
}

func TestPersistCardEventBoundsFailuresAndHonorsCancellation(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		calls := 0
		_, err := persistCardEvent(context.Background(), func(context.Context) (string, error) {
			calls++
			if unknown {
				return "", pgx.ErrNoRows
			}
			return "", errors.New("database offline")
		}, time.Microsecond)
		want := 5
		if unknown {
			want = 1
		}
		if calls != want || err == nil {
			t.Fatal(calls, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := persistCardEvent(ctx, func(context.Context) (string, error) {
		cancel()
		return "", errors.New("offline")
	}, time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
