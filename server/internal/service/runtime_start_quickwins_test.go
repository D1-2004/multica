package service

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func quickwinFixture(t *testing.T) (*pgxpool.Pool, *TaskService, db.AgentTaskQueue, db.AgentTaskRuntimeStartAttempt) {
	t.Helper()
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	q := db.New(pool)
	svc := NewTaskService(q, pool, nil, events.New())
	id, _, _ := dispatchedCommentTaskFixture(t, ctx, pool)
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status='queued', dispatched_at=NULL,created_at=now()-interval '1 hour' WHERE id=$1`, util.MustParseUUID(id)); err != nil {
		t.Fatal(err)
	}
	task, err := q.GetAgentTask(ctx, util.MustParseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := svc.BeginRuntimeStartAttempt(ctx, task, SandboxBackendAliyunFC, RuntimeStartProtocolHTTPJSONV1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM agent_task_runtime_start_attempt WHERE task_id=$1`, task.ID)
	})
	return pool, svc, task, attempt
}

func TestPerformanceRolloutDSHRecoveryDoesNotCrossAgentBoundary(t *testing.T) {
	ctx := context.Background()
	pool, svc, task, attempt := quickwinFixture(t)
	if _, err := svc.RecordRuntimeStartStage(ctx, attempt.ID, task.ID, task.RuntimeID, "dsh_host_waiting"); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkRuntimeStartBlocked(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	other := pgtype.UUID{Bytes: [16]byte{91}, Valid: true}
	for _, tc := range []struct {
		name string
		ids  []pgtype.UUID
		want bool
	}{
		{"empty_targets", nil, false},
		{"other_agent", []pgtype.UUID{other}, false},
		{"selected_agent", []pgtype.UUID{task.AgentID}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := svc.Queries.ListDSHHostWaitingTasks(ctx, db.ListDSHHostWaitingTasksParams{DshEventWakeup: true, Scoped: true, RolloutAgentIDs: tc.ids})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, row := range rows {
				found = found || row.ID == task.ID
			}
			if found != tc.want {
				t.Fatalf("early recovery selected=%v, want %v", found, tc.want)
			}
		})
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_task_runtime_start_attempt SET finished_at=now()-interval '11 minutes' WHERE id=$1`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if rows, err := svc.Queries.ExpireDSHHostWaitingTasks(ctx, true, nil); err != nil || len(rows) != 0 {
		t.Fatalf("empty rollout expired a task: rows=%d err=%v", len(rows), err)
	}
	if rows, err := svc.Queries.ExpireDSHHostWaitingTasks(ctx, true, []pgtype.UUID{other}); err != nil || len(rows) != 0 {
		t.Fatalf("other agent expired by rollout: rows=%d err=%v", len(rows), err)
	}
	if rows, err := svc.Queries.ExpireDSHHostWaitingTasks(ctx, true, []pgtype.UUID{task.AgentID}); err != nil || len(rows) != 1 || rows[0].ID != task.ID {
		t.Fatalf("selected agent did not expire: rows=%v err=%v", rows, err)
	}
}

func TestFCAbandonedLaunchRecoveryRollout(t *testing.T) {
	for _, tc := range []struct {
		name, stage, lease, receipt string
		enabled, want               bool
	}{
		{"off", "sandbox_resolving", "NULL", "NULL", false, false},
		{"expired", "sandbox_resolving", "now()-interval '1 second'", "NULL", true, true},
		{"template", "template_resolved", "NULL", "NULL", true, true},
		{"live", "sandbox_resolving", "now()+interval '1 minute'", "NULL", true, false},
		{"uncertain_submission", "runner_exec_submitting", "NULL", "NULL", true, false},
		{"environment", "task_environment_preparing", "NULL", "NULL", true, true},
		{"submitted", "runner_exec_submitted", "NULL", "NULL", true, false},
		{"receipt", "sandbox_resolving", "NULL", "now()", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool, svc, task, attempt := quickwinFixture(t)
			if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET runtime_launch_lease_expires_at=`+tc.lease+` WHERE id=$1`, task.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE agent_task_runtime_start_attempt SET last_stage=$2, runner_started_at=`+tc.receipt+` WHERE id=$1`, attempt.ID, tc.stage); err != nil {
				t.Fatal(err)
			}
			rows, err := svc.Queries.ListDSHHostWaitingTasks(ctx, db.ListDSHHostWaitingTasksParams{RecoverAbandonedLaunches: tc.enabled})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, row := range rows {
				found = found || row.ID == task.ID
			}
			if found != tc.want {
				t.Fatalf("selected=%v want=%v", found, tc.want)
			}
		})
	}
}

func TestPerformanceRolloutFCRecoveryDoesNotCrossAgentBoundary(t *testing.T) {
	ctx := context.Background()
	pool, svc, task, attempt := quickwinFixture(t)
	if _, err := pool.Exec(ctx, `UPDATE agent_task_runtime_start_attempt SET last_stage='template_resolved' WHERE id=$1`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		ids  []pgtype.UUID
		want bool
	}{
		{"empty_targets", nil, false},
		{"other_agent", []pgtype.UUID{{Bytes: [16]byte{93}, Valid: true}}, false},
		{"selected_agent", []pgtype.UUID{task.AgentID}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := svc.Queries.ListDSHHostWaitingTasks(ctx, db.ListDSHHostWaitingTasksParams{
				RecoverAbandonedLaunches: true, Scoped: true, RolloutAgentIDs: tc.ids,
			})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, row := range rows {
				found = found || row.ID == task.ID
			}
			if found != tc.want {
				t.Fatalf("abandoned FC recovery selected=%v want=%v", found, tc.want)
			}
		})
	}
}

func TestDSHWaitExpiryAcrossRetriesAndClaimFences(t *testing.T) {
	for _, tc := range []struct {
		name, firstAge, latestStage, lease string
		locked, token, want                bool
	}{
		{"old_queue_recent_wait", "1 minute", "dsh_host_waiting", "NULL", false, false, false},
		{"repeated_wait", "11 minutes", "dsh_host_waiting", "NULL", false, false, true},
		{"different_latest", "11 minutes", "sandbox_resolving", "NULL", false, false, false},
		{"live_launcher", "11 minutes", "dsh_host_waiting", "now()+interval '1 minute'", false, false, false},
		{"claim_transaction", "11 minutes", "dsh_host_waiting", "NULL", true, false, false},
		{"claim_token", "11 minutes", "dsh_host_waiting", "NULL", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool, svc, task, attempt := quickwinFixture(t)
			if _, err := svc.RecordRuntimeStartStage(ctx, attempt.ID, task.ID, task.RuntimeID, "dsh_host_waiting"); err != nil {
				t.Fatal(err)
			}
			if err := svc.MarkRuntimeStartBlocked(ctx, attempt); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE agent_task_runtime_start_attempt SET created_at=now()-interval '15 minutes', finished_at=now()-$2::interval WHERE id=$1`, attempt.ID, tc.firstAge); err != nil {
				t.Fatal(err)
			}
			latest, err := svc.BeginRuntimeStartAttempt(ctx, task, SandboxBackendAliyunFC, RuntimeStartProtocolHTTPJSONV1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = svc.RecordRuntimeStartStage(ctx, latest.ID, task.ID, task.RuntimeID, tc.latestStage); err != nil {
				t.Fatal(err)
			}
			if err = svc.MarkRuntimeStartBlocked(ctx, latest); err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, `UPDATE agent_task_queue SET runtime_launch_lease_expires_at=`+tc.lease+` WHERE id=$1`, task.ID); err != nil {
				t.Fatal(err)
			}
			if tc.token {
				var owner, ws string
				if err = pool.QueryRow(ctx, `SELECT owner_id,workspace_id FROM agent_runtime WHERE id=$1`, task.RuntimeID).Scan(&owner, &ws); err != nil {
					t.Fatal(err)
				}
				if _, err = svc.Queries.CreateTaskToken(ctx, runtimeStartClaimTokenParams(task, owner, ws, "quickwin")); err != nil {
					t.Fatal(err)
				}
			}
			if tc.locked {
				tx, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(ctx)
				if _, err = tx.Exec(ctx, `SELECT id FROM agent_task_queue WHERE id=$1 FOR UPDATE`, task.ID); err != nil {
					t.Fatal(err)
				}
			}
			rows, err := svc.Queries.ExpireDSHHostWaitingTasks(ctx, false, nil)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, row := range rows {
				found = found || row.ID == task.ID
			}
			if found != tc.want {
				t.Fatalf("expired=%v want=%v", found, tc.want)
			}
			got, err := svc.Queries.GetAgentTask(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want && (got.Status != "failed" || got.FailureReason.String != "runtime_start_failed") {
				t.Fatalf("unexpected status %s", got.Status)
			}
			if !tc.want && got.Status != "queued" {
				t.Fatalf("protected task changed: %s", got.Status)
			}
		})
	}
}

func TestDSHWaitExpirySwitchOffThenOn(t *testing.T) {
	ctx := context.Background()
	pool, svc, task, attempt := quickwinFixture(t)
	if _, err := svc.RecordRuntimeStartStage(ctx, attempt.ID, task.ID, task.RuntimeID, "dsh_host_waiting"); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkRuntimeStartBlocked(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_task_runtime_start_attempt SET finished_at=now()-interval '11 minutes' WHERE id=$1`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	enabled := false
	svc.RuntimeStartRecoveryConfig = func() RuntimeStartRecoveryConfig { return RuntimeStartRecoveryConfig{BoundDSHHostWait: enabled} }
	svc.RuntimeLauncher = &capacityWakeRuntimeLauncher{}
	// Serialization is not part of this test; no launcher lease means a nudge is a no-op.
	svc.runtimeLaunchLeases = nil
	svc.RecoverWaitingDSHHosts(ctx)
	before, err := svc.Queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Status != "queued" {
		t.Fatalf("disabled switch expired task: %s", before.Status)
	}
	enabled = true
	svc.RecoverWaitingDSHHosts(ctx)
	after, err := svc.Queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "failed" {
		t.Fatalf("enabled switch did not expire task: %s", after.Status)
	}
	final, err := svc.Queries.GetAgentTaskRuntimeStartAttempt(ctx, db.GetAgentTaskRuntimeStartAttemptParams{ID: attempt.ID, TaskID: task.ID, RuntimeID: task.RuntimeID})
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != "failed" || final.ErrorCode != "DSH-HOST-WAIT-TIMEOUT" {
		t.Fatalf("attempt not finalized: %s %s", final.Status, final.ErrorCode)
	}
}
