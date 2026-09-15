package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/dshschedule"
)

type emptyScheduleQueue struct{}

func (emptyScheduleQueue) Candidates(context.Context, int) ([]dshschedule.Candidate, error) {
	return nil, nil
}
func (emptyScheduleQueue) Defer(context.Context, dshschedule.Candidate) error { return nil }
func (emptyScheduleQueue) DispatchDSHSchedule(context.Context, dshschedule.Key) (dshschedule.Receipt, error) {
	panic("empty queue must not dispatch")
}

func TestDSHScheduleJobUsesDurableDiscoveryAfterOutage(t *testing.T) {
	job := DSHScheduleDispatchJob(emptyScheduleQueue{}, emptyScheduleQueue{})
	if err := job.validate(); err != nil {
		t.Fatal(err)
	}
	if job.CatchUpMode != CatchUpLatestOnly || !job.AllowStaleReentry {
		t.Fatal("invalid restart policy")
	}
	// Even a stale scan opportunity queries the reminder ledger. The Autopilot
	// cron lateness cutoff must never discard a late DSH one-shot reminder.
	result, err := job.Handler(context.Background(), HandlerInput{PlanTime: time.Now().Add(-72 * time.Hour)})
	if err != nil || result.RowsAffected != 0 || result.Result["examined"] != 0 {
		t.Fatal(result, err)
	}
}
