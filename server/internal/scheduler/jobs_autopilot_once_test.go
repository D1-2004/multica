package scheduler

import (
	"context"
	"testing"
	"time"
)

// One-shot planning must recover the absolute occurrence after outages, while
// retries keep its identity and edits never replay the superseded time.
func TestOnceSchedulePlanRecoveryAndSupersession(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	due := now.Add(-48 * time.Hour)
	cases := []struct {
		name     string
		due      time.Time
		consumed bool
		latest   LatestPlanInfo
		want     bool
	}{
		{name: "outage beyond cron catch-up window", due: due, want: true},
		{name: "future", due: now.Add(time.Minute)},
		{name: "success remains consumed", due: due, consumed: true, latest: LatestPlanInfo{Found: true, PlanTime: due, Status: "SUCCESS"}},
		{name: "post-commit crash retries same receipt", due: due, consumed: true, latest: LatestPlanInfo{Found: true, PlanTime: due, Status: "FAILED", Attempt: 1, MaxAttempts: 3}, want: true},
		{name: "failure exhausted never schedules another year", due: due, latest: LatestPlanInfo{Found: true, PlanTime: due, Status: "FAILED", Attempt: 3, MaxAttempts: 3}},
		{name: "retry backoff", due: due, latest: LatestPlanInfo{Found: true, PlanTime: due, Status: "FAILED", Attempt: 1, MaxAttempts: 3, NextRetryAt: now.Add(time.Minute)}},
		{name: "edited earlier time supersedes old failed cursor", due: due.Add(-time.Hour), latest: LatestPlanInfo{Found: true, PlanTime: due, Status: "FAILED", Attempt: 1, MaxAttempts: 3}, want: true},
		{name: "consumed without scheduler history", due: due, consumed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cache := newAutopilotScheduleCache()
			cfg := autopilotTriggerConfig{Kind: "once", RunAt: tc.due}
			if tc.consumed {
				cfg.LastFiredAt = now
			}
			cache.replace(map[string]autopilotTriggerConfig{"once": cfg})
			plans, err := autopilotPlansForScope(cache)(context.Background(), Scope{ID: "once"}, now, tc.latest)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want {
				if len(plans) != 1 || !plans[0].Equal(tc.due) {
					t.Fatalf("plans=%v, want %v", plans, tc.due)
				}
			} else if len(plans) != 0 {
				t.Fatalf("unexpected plans %v", plans)
			}
		})
	}
}
