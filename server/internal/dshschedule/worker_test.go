package dshschedule

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeWorkQueue struct {
	candidates        []Candidate
	deferred          []Candidate
	err, errorOnDefer error
}

func (q *fakeWorkQueue) Candidates(context.Context, int) ([]Candidate, error) {
	return q.candidates, q.err
}
func (q *fakeWorkQueue) Defer(_ context.Context, c Candidate) error {
	q.deferred = append(q.deferred, c)
	return q.errorOnDefer
}

type dispatchFunc func(context.Context, Key) (Receipt, error)

func (f dispatchFunc) DispatchDSHSchedule(ctx context.Context, key Key) (Receipt, error) {
	return f(ctx, key)
}

func TestScheduleSweepContinuesPastDeniedAndLostRace(t *testing.T) {
	r := fixture()
	q := &fakeWorkQueue{}
	for _, id := range []string{"schedule-1", "schedule-2", "schedule-3"} {
		key := r.Key
		key.ScheduleID = id
		q.candidates = append(q.candidates, Candidate{Key: key, NextDue: r.FirstDue})
	}
	seen := 0
	result, err := Sweep(context.Background(), q, dispatchFunc(func(ctx context.Context, key Key) (Receipt, error) {
		seen++
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 10*time.Second {
			t.Fatal("unbounded candidate")
		}
		switch key.ScheduleID {
		case "schedule-1":
			return Receipt{}, errors.New("current permission denied")
		case "schedule-2":
			return Receipt{}, ErrNotDue
		default:
			return Receipt{TaskID: r.SourceTaskID}, nil
		}
	}))
	if err != nil || seen != 3 || result != (SweepResult{Examined: 3, Admitted: 1, Deferred: 1, Skipped: 1}) || len(q.deferred) != 1 || q.deferred[0] != q.candidates[0] {
		t.Fatalf("result=%+v err=%v deferred=%v", result, err, len(q.deferred))
	}
}

func TestScheduleSweepCancellationAndBookkeepingFailure(t *testing.T) {
	q := &fakeWorkQueue{candidates: []Candidate{{Key: fixture().Key}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	dispatch := dispatchFunc(func(context.Context, Key) (Receipt, error) { calls++; return Receipt{}, errors.New("failed") })
	if _, err := Sweep(ctx, q, dispatch); !errors.Is(err, context.Canceled) || calls != 0 || len(q.deferred) != 0 {
		t.Fatal("cancelled sweep wrote or admitted")
	}
	sentinel := errors.New("retry persistence unavailable")
	q.errorOnDefer = sentinel
	if _, err := Sweep(context.Background(), q, dispatch); !errors.Is(err, sentinel) {
		t.Fatal("unconfirmed retry was hidden", err)
	}
	q.err = sentinel
	if _, err := Sweep(context.Background(), q, dispatch); !errors.Is(err, sentinel) || calls != 1 {
		t.Fatal("scan failure was hidden", err)
	}
}
