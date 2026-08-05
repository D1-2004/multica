package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func runtimeStartClaimTokenParams(task db.AgentTaskQueue, userID, workspaceID, suffix string) db.CreateTaskTokenParams {
	return db.CreateTaskTokenParams{
		TokenHash:   fmt.Sprintf("runtime-start-%s-%d", suffix, time.Now().UnixNano()),
		TaskID:      task.ID,
		AgentID:     task.AgentID,
		WorkspaceID: util.MustParseUUID(workspaceID),
		UserID:      util.MustParseUUID(userID),
		ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}
}

func TestNewRuntimeLaunchLeaseSupersedesAbandonedAttempt(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())
	taskID, _, _ := dispatchedCommentTaskFixture(t, ctx, pool)
	taskUUID := util.MustParseUUID(taskID)
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'queued', dispatched_at = NULL
		WHERE id = $1
	`, taskUUID); err != nil {
		t.Fatalf("reset task to queued: %v", err)
	}
	task, err := queries.GetAgentTask(ctx, taskUUID)
	if err != nil {
		t.Fatalf("load queued task: %v", err)
	}
	store := newPostgresTaskRuntimeLaunchLeaseStore(queries)
	firstLease, acquired, err := store.Acquire(ctx, task.ID, runtimeLaunchLeaseDuration)
	if err != nil || !acquired {
		t.Fatalf("acquire first launch lease: acquired=%v err=%v", acquired, err)
	}
	firstAttempt, err := svc.BeginRuntimeStartAttempt(
		withTaskRuntimeLaunchLease(ctx, firstLease),
		task,
		SandboxBackendAliyunFC,
		RuntimeStartProtocolHTTPJSONV1,
	)
	if err != nil {
		t.Fatalf("begin first attempt: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue
		SET runtime_launch_lease_expires_at = now() - interval '1 second'
		WHERE id = $1
	`, task.ID); err != nil {
		t.Fatalf("expire first launch lease: %v", err)
	}
	if _, err := svc.BeginRuntimeStartAttempt(
		withTaskRuntimeLaunchLease(ctx, firstLease),
		task,
		SandboxBackendAliyunFC,
		RuntimeStartProtocolHTTPJSONV1,
	); !errors.Is(err, errRuntimeLaunchLeaseLost) {
		t.Fatalf("expired lease begin error = %v, want lease lost", err)
	}
	secondLease, acquired, err := store.Acquire(ctx, task.ID, runtimeLaunchLeaseDuration)
	if err != nil || !acquired {
		t.Fatalf("acquire replacement launch lease: acquired=%v err=%v", acquired, err)
	}
	secondAttempt, err := svc.BeginRuntimeStartAttempt(
		withTaskRuntimeLaunchLease(ctx, secondLease),
		task,
		SandboxBackendAliyunFC,
		RuntimeStartProtocolHTTPJSONV1,
	)
	if err != nil {
		t.Fatalf("begin replacement attempt: %v", err)
	}
	if firstAttempt.ID == secondAttempt.ID {
		t.Fatal("replacement reused abandoned attempt id")
	}
	gotFirst, err := queries.GetAgentTaskRuntimeStartAttempt(ctx, db.GetAgentTaskRuntimeStartAttemptParams{
		ID: firstAttempt.ID, TaskID: task.ID, RuntimeID: task.RuntimeID,
	})
	if err != nil {
		t.Fatalf("load abandoned attempt: %v", err)
	}
	if gotFirst.Status != "superseded" || gotFirst.LastStage != "launch_restarted" || !gotFirst.FinishedAt.Valid {
		t.Fatalf("abandoned attempt after replacement = %+v", gotFirst)
	}
	if secondAttempt.Status != "starting" || secondAttempt.LastStage != "launch_started" {
		t.Fatalf("replacement attempt = %+v", secondAttempt)
	}
}

func TestRuntimeStartAttemptCanFollowWarmClaimWithHeldLease(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())
	taskID, userID, workspaceID := dispatchedCommentTaskFixture(t, ctx, pool)
	taskUUID := util.MustParseUUID(taskID)
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'queued', dispatched_at = NULL
		WHERE id = $1
	`, taskUUID); err != nil {
		t.Fatalf("reset task to queued: %v", err)
	}
	queued, err := queries.GetAgentTask(ctx, taskUUID)
	if err != nil {
		t.Fatalf("load queued task: %v", err)
	}
	store := newPostgresTaskRuntimeLaunchLeaseStore(queries)
	lease, acquired, err := store.Acquire(ctx, queued.ID, runtimeLaunchLeaseDuration)
	if err != nil || !acquired {
		t.Fatalf("acquire launch lease: acquired=%v err=%v", acquired, err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'dispatched', dispatched_at = now()
		WHERE id = $1
	`, queued.ID); err != nil {
		t.Fatalf("simulate warm daemon claim: %v", err)
	}
	dispatched, err := queries.GetAgentTask(ctx, queued.ID)
	if err != nil {
		t.Fatalf("load dispatched task: %v", err)
	}
	attempt, err := svc.BeginRuntimeStartAttempt(
		withTaskRuntimeLaunchLease(ctx, lease),
		dispatched,
		SandboxBackendAliyunFC,
		RuntimeStartProtocolLegacyV1,
	)
	if err != nil {
		t.Fatalf("begin attempt after warm claim: %v", err)
	}
	if _, err := svc.FinalizeTaskClaim(
		ctx,
		dispatched,
		runtimeStartClaimTokenParams(dispatched, userID, workspaceID, "warm-claim"),
		nil,
		false,
	); err != nil {
		t.Fatalf("finalize warm claim: %v", err)
	}
	got, err := queries.GetAgentTaskRuntimeStartAttempt(ctx, db.GetAgentTaskRuntimeStartAttemptParams{
		ID: attempt.ID, TaskID: dispatched.ID, RuntimeID: dispatched.RuntimeID,
	})
	if err != nil {
		t.Fatalf("load warm-claim attempt: %v", err)
	}
	if got.Status != "claimed" {
		t.Fatalf("warm-claim attempt status = %q, want claimed", got.Status)
	}
}

func TestLegacyRuntimeClaimFinalizesAttemptWithoutProtocolFields(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())
	taskID, userID, workspaceID := dispatchedCommentTaskFixture(t, ctx, pool)
	task, err := queries.GetAgentTask(ctx, util.MustParseUUID(taskID))
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	attempt, err := svc.BeginRuntimeStartAttempt(ctx, task, SandboxBackendAliyunFC, RuntimeStartProtocolLegacyV1)
	if err != nil {
		t.Fatalf("begin legacy attempt: %v", err)
	}

	if _, err := svc.FinalizeTaskClaim(
		ctx,
		task,
		runtimeStartClaimTokenParams(task, userID, workspaceID, "legacy"),
		nil,
		false,
	); err != nil {
		t.Fatalf("finalize legacy claim: %v", err)
	}
	got, err := queries.GetAgentTaskRuntimeStartAttempt(ctx, db.GetAgentTaskRuntimeStartAttemptParams{
		ID: attempt.ID, TaskID: task.ID, RuntimeID: task.RuntimeID,
	})
	if err != nil {
		t.Fatalf("load attempt: %v", err)
	}
	if got.Status != "claimed" || got.LastStage != "claim_finalized" || !got.ClaimFinalizedAt.Valid {
		t.Fatalf("legacy attempt after claim = %+v", got)
	}
}

func TestOldBackendTaskTokenIsCrossVersionClaimProof(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())
	taskID, userID, workspaceID := dispatchedCommentTaskFixture(t, ctx, pool)
	task, err := queries.GetAgentTask(ctx, util.MustParseUUID(taskID))
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	attempt, err := svc.BeginRuntimeStartAttempt(ctx, task, SandboxBackendAliyunFC, RuntimeStartProtocolHTTPJSONV1)
	if err != nil {
		t.Fatalf("begin attempt: %v", err)
	}

	// Simulate an old backend replica: it ignores the additive request fields
	// and writes only the task token at the final claim boundary.
	if _, err := queries.CreateTaskToken(ctx, runtimeStartClaimTokenParams(task, userID, workspaceID, "old-backend")); err != nil {
		t.Fatalf("create old-backend token: %v", err)
	}
	lateFailure := NewRuntimeStartFailure(
		SandboxBackendAliyunFC,
		"FCE2B-RUNNER-CLAIM-TIMEOUT",
		"claim_wait",
		true,
		"Runner 未在规定时间内完成任务领取。",
		"old backend claim response raced the launcher timeout",
	)
	if gotTask, err := svc.FailTaskRuntimeStart(ctx, task.ID, task.RuntimeID, attempt.ID, lateFailure); err != nil {
		t.Fatalf("old-backend token versus startup failure: %v", err)
	} else if gotTask.Status != "dispatched" {
		t.Fatalf("startup failure overrode old-backend claim proof: status=%q", gotTask.Status)
	}
	got, err := queries.FinalizeAgentTaskRuntimeStartAttemptForTask(ctx, db.FinalizeAgentTaskRuntimeStartAttemptForTaskParams{
		TaskID: task.ID, RuntimeID: task.RuntimeID,
	})
	if err != nil {
		t.Fatalf("finalize from old-backend token: %v", err)
	}
	if got.ID != attempt.ID || got.Status != "claimed" {
		t.Fatalf("finalized attempt = %+v, want %s claimed", got, util.UUIDToString(attempt.ID))
	}
}

func TestLateRunnerExecBookkeepingDoesNotRegressRunnerStartedStage(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())
	taskID, _, _ := dispatchedCommentTaskFixture(t, ctx, pool)
	task, err := queries.GetAgentTask(ctx, util.MustParseUUID(taskID))
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	attempt, err := svc.BeginRuntimeStartAttempt(ctx, task, SandboxBackendAliyunFC, RuntimeStartProtocolHTTPJSONV1)
	if err != nil {
		t.Fatalf("begin attempt: %v", err)
	}
	if _, err := svc.RecordRuntimeStartStage(ctx, attempt.ID, task.ID, task.RuntimeID, "runner_started"); err != nil {
		t.Fatalf("record runner started: %v", err)
	}
	if err := svc.RecordRuntimeStartRunnerExecSubmitted(ctx, attempt.ID, task.ID, task.RuntimeID); err != nil {
		t.Fatalf("record late runner exec bookkeeping: %v", err)
	}
	got, err := queries.GetAgentTaskRuntimeStartAttempt(ctx, db.GetAgentTaskRuntimeStartAttemptParams{
		ID: attempt.ID, TaskID: task.ID, RuntimeID: task.RuntimeID,
	})
	if err != nil {
		t.Fatalf("load attempt: %v", err)
	}
	if got.LastStage != "runner_started" || !got.RunnerStartedAt.Valid {
		t.Fatalf("late bookkeeping regressed attempt = %+v", got)
	}
}

func TestRuntimeStartFailureCannotOverrideFinalizedClaim(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())
	taskID, userID, workspaceID := dispatchedCommentTaskFixture(t, ctx, pool)
	task, err := queries.GetAgentTask(ctx, util.MustParseUUID(taskID))
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	attempt, err := svc.BeginRuntimeStartAttempt(ctx, task, SandboxBackendAliyunFC, RuntimeStartProtocolHTTPJSONV1)
	if err != nil {
		t.Fatalf("begin attempt: %v", err)
	}
	if _, err := svc.FinalizeTaskClaim(
		ctx,
		task,
		runtimeStartClaimTokenParams(task, userID, workspaceID, "claim-wins"),
		nil,
		false,
	); err != nil {
		t.Fatalf("finalize claim: %v", err)
	}

	failure := NewRuntimeStartFailure(
		SandboxBackendAliyunFC,
		"FCE2B-DAEMON-START-FAILED",
		"daemon_start",
		false,
		"Runtime 启动失败。",
		"late failure report",
	)
	gotTask, err := svc.FailTaskRuntimeStart(ctx, task.ID, task.RuntimeID, attempt.ID, failure)
	if err != nil {
		t.Fatalf("late failure report: %v", err)
	}
	if gotTask.Status != "dispatched" {
		t.Fatalf("late failure changed task status to %q", gotTask.Status)
	}
	gotAttempt, err := queries.GetAgentTaskRuntimeStartAttempt(ctx, db.GetAgentTaskRuntimeStartAttemptParams{
		ID: attempt.ID, TaskID: task.ID, RuntimeID: task.RuntimeID,
	})
	if err != nil {
		t.Fatalf("load attempt: %v", err)
	}
	if gotAttempt.Status != "claimed" {
		t.Fatalf("late failure changed attempt status to %q", gotAttempt.Status)
	}
}

func TestClaimCannotOverrideRuntimeStartFailure(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())
	taskID, userID, workspaceID := dispatchedCommentTaskFixture(t, ctx, pool)
	task, err := queries.GetAgentTask(ctx, util.MustParseUUID(taskID))
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	attempt, err := svc.BeginRuntimeStartAttempt(ctx, task, SandboxBackendAliyunFC, RuntimeStartProtocolHTTPJSONV1)
	if err != nil {
		t.Fatalf("begin attempt: %v", err)
	}
	failure := NewRuntimeStartFailure(
		SandboxBackendAliyunFC,
		"FCE2B-DAEMON-START-FAILED",
		"daemon_start",
		false,
		"Runtime 启动失败。",
		"startup failed before claim",
	)
	if _, err := svc.FailTaskRuntimeStart(ctx, task.ID, task.RuntimeID, attempt.ID, failure); err != nil {
		t.Fatalf("fail startup: %v", err)
	}

	if _, err := svc.FinalizeTaskClaim(
		ctx,
		task,
		runtimeStartClaimTokenParams(task, userID, workspaceID, "failure-wins"),
		nil,
		false,
	); err == nil {
		t.Fatal("claim finalized after Runtime startup failure")
	}
	var tokenCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM task_token WHERE task_id = $1`, task.ID).Scan(&tokenCount); err != nil {
		t.Fatalf("count task tokens: %v", err)
	}
	if tokenCount != 0 {
		t.Fatalf("startup-failed task has %d task tokens", tokenCount)
	}
}
