package scheduler

import (
	"context"
	"time"

	"github.com/multica-ai/multica/server/internal/dshschedule"
)

// A plan is only a scan opportunity. The reminder ledger, not the cron bucket,
// owns occurrence identity and lateness semantics. Restarting skips obsolete
// scan buckets but still discovers every active overdue reminder.
func DSHScheduleDispatchJob(queue dshschedule.WorkQueue, dispatcher dshschedule.Dispatcher) JobSpec {
	return JobSpec{
		Name: "dsh_schedule_dispatch", Cadence: 30 * time.Second, CatchUpMode: CatchUpLatestOnly,
		CatchUpWindow: time.Hour, MaxPlansPerTick: 1, RunTimeout: 20 * time.Second,
		StaleTimeout: time.Minute, HeartbeatInterval: 10 * time.Second,
		AllowStaleReentry: true, MaxAttempts: 3, RetryBackoff: []time.Duration{30 * time.Second, time.Minute},
		Scopes: StaticScopes(ScopeGlobal),
		Handler: func(ctx context.Context, _ HandlerInput) (HandlerResult, error) {
			result, err := dshschedule.Sweep(ctx, queue, dispatcher)
			return HandlerResult{RowsAffected: int64(result.Admitted), Result: map[string]any{
				"examined": result.Examined, "admitted": result.Admitted, "deferred": result.Deferred, "skipped": result.Skipped,
			}}, err
		},
	}
}
