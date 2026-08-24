package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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

func TestRuntimeStartFailurePersistsAfterRequestCancellation(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())
	taskID, _, _ := dispatchedCommentTaskFixture(t, ctx, pool)
	task, err := queries.GetAgentTask(ctx, util.MustParseUUID(taskID))
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	attempt, err := svc.BeginRuntimeStartAttempt(
		ctx,
		task,
		SandboxBackendASB,
		RuntimeStartProtocolHTTPJSONV1,
	)
	if err != nil {
		t.Fatalf("begin attempt: %v", err)
	}
	if _, err := svc.RecordRuntimeStartStage(
		ctx,
		attempt.ID,
		task.ID,
		task.RuntimeID,
		"sandbox_resolving",
	); err != nil {
		t.Fatalf("record sandbox resolving stage: %v", err)
	}

	requestCtx, cancelRequest := context.WithCancel(ctx)
	cancelRequest()
	failure := ClassifyRuntimeStartError(SandboxBackendASB, requestCtx.Err())
	failedTask, err := svc.FailTaskRuntimeStart(
		requestCtx,
		task.ID,
		task.RuntimeID,
		attempt.ID,
		failure,
	)
	if err != nil {
		t.Fatalf("persist failure after request cancellation: %v", err)
	}
	if failedTask.Status != "failed" {
		t.Fatalf("task status = %q, want failed", failedTask.Status)
	}

	gotAttempt, err := queries.GetAgentTaskRuntimeStartAttempt(ctx, db.GetAgentTaskRuntimeStartAttemptParams{
		ID:        attempt.ID,
		TaskID:    task.ID,
		RuntimeID: task.RuntimeID,
	})
	if err != nil {
		t.Fatalf("load failed attempt: %v", err)
	}
	if gotAttempt.Status != "failed" ||
		gotAttempt.LastStage != "sandbox_resolving" ||
		gotAttempt.ErrorCode != "ASB-SANDBOX-RESOLVING-FAILED" {
		t.Fatalf("failed attempt = %+v", gotAttempt)
	}
}

func TestASBCapacityWaitKeepsTaskQueuedAndUsesLatestAttempt(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())
	taskID, _, _ := dispatchedCommentTaskFixture(t, ctx, pool)
	taskUUID := util.MustParseUUID(taskID)
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'queued',
		    dispatched_at = NULL,
		    completed_at = NULL,
		    error = NULL,
		    failure_reason = NULL,
		    created_at = now() - interval '3 hours'
		WHERE id = $1
	`, taskUUID); err != nil {
		t.Fatalf("reset task to queued: %v", err)
	}
	task, err := queries.GetAgentTask(ctx, taskUUID)
	if err != nil {
		t.Fatalf("load queued task: %v", err)
	}

	capacityAttempt, err := svc.BeginRuntimeStartAttempt(
		ctx,
		task,
		SandboxBackendASB,
		RuntimeStartProtocolHTTPJSONV1,
	)
	if err != nil {
		t.Fatalf("begin capacity attempt: %v", err)
	}
	if _, err := svc.RecordRuntimeStartStage(
		ctx,
		capacityAttempt.ID,
		task.ID,
		task.RuntimeID,
		"sandbox_resolving",
	); err != nil {
		t.Fatalf("record sandbox resolving: %v", err)
	}
	if waiting, err := svc.MarkRuntimeStartCapacityWaiting(ctx, capacityAttempt); err != nil || !waiting {
		t.Fatalf("mark capacity waiting: %v", err)
	}

	queued, err := queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("load capacity-waiting task: %v", err)
	}
	if queued.Status != "queued" || queued.CompletedAt.Valid || queued.Error.Valid || queued.FailureReason.Valid {
		t.Fatalf("capacity-waiting task became terminal: %+v", queued)
	}
	gotAttempt, err := queries.GetAgentTaskRuntimeStartAttempt(ctx, db.GetAgentTaskRuntimeStartAttemptParams{
		ID: capacityAttempt.ID, TaskID: task.ID, RuntimeID: task.RuntimeID,
	})
	if err != nil {
		t.Fatalf("load capacity attempt: %v", err)
	}
	if gotAttempt.Status != "blocked" ||
		gotAttempt.LastStage != asbCapacityWaitingStage ||
		gotAttempt.ErrorCode != asbCapacityWaitingErrorCode ||
		!gotAttempt.FinishedAt.Valid {
		t.Fatalf("capacity attempt = %+v", gotAttempt)
	}

	waiting, err := queries.ListASBCapacityWaitingTasks(ctx, db.ListASBCapacityWaitingTasksParams{
		RetrySeconds: 0,
		StaleSeconds: time.Hour.Seconds(),
	})
	if err != nil {
		t.Fatalf("list capacity waits: %v", err)
	}
	if len(waiting) != 1 || waiting[0].ID != task.ID {
		t.Fatalf("capacity waits = %+v, want task %s", waiting, taskID)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue
		SET context = context || '{"deap_dws_token_required":true}'::jsonb
		WHERE id = $1
	`, task.ID); err != nil {
		t.Fatalf("mark request-bound A2A task: %v", err)
	}
	waiting, err = queries.ListASBCapacityWaitingTasks(ctx, db.ListASBCapacityWaitingTasksParams{
		RetrySeconds: 0,
		StaleSeconds: time.Hour.Seconds(),
	})
	if err != nil {
		t.Fatalf("list request-bound capacity waits: %v", err)
	}
	for _, candidate := range waiting {
		if candidate.ID == task.ID {
			t.Fatal("request-bound DEAP DWS task entered durable capacity retry")
		}
	}
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue
		SET context = context - 'deap_dws_token_required'
		WHERE id = $1
	`, task.ID); err != nil {
		t.Fatalf("restore durable task context: %v", err)
	}

	// The generic queued backlog sweeper must not expire an intentional ASB
	// capacity wait, even though this fixture predates the normal two-hour TTL.
	expired, err := queries.ExpireStaleQueuedTasks(ctx, db.ExpireStaleQueuedTasksParams{
		TtlSecs:            1,
		AsbCapacityTtlSecs: 24 * time.Hour.Seconds(),
		MaxPerTick:         10,
	})
	if err != nil {
		t.Fatalf("expire queues with capacity waiter: %v", err)
	}
	for _, candidate := range expired {
		if candidate.ID == task.ID {
			t.Fatal("capacity-waiting task was expired by generic queue TTL")
		}
	}

	// A newer serialization block supersedes the historical capacity record.
	// The capacity worker must stop selecting it, and normal queue TTL applies.
	leaseStore := newPostgresTaskRuntimeLaunchLeaseStore(queries)
	lease, acquired, err := leaseStore.Acquire(ctx, task.ID, runtimeLaunchLeaseDuration)
	if err != nil || !acquired {
		t.Fatalf("acquire capacity retry lease: acquired=%v err=%v", acquired, err)
	}
	serializationAttempt, err := svc.BeginRuntimeStartAttempt(
		withTaskRuntimeLaunchLease(ctx, lease),
		task,
		SandboxBackendASB,
		RuntimeStartProtocolHTTPJSONV1,
	)
	if err != nil {
		t.Fatalf("begin newer serialization attempt: %v", err)
	}
	expired, err = queries.ExpireStaleQueuedTasks(ctx, db.ExpireStaleQueuedTasksParams{
		TtlSecs:            1,
		AsbCapacityTtlSecs: 24 * time.Hour.Seconds(),
		MaxPerTick:         10,
	})
	if err != nil {
		t.Fatalf("expire queue during capacity retry: %v", err)
	}
	for _, candidate := range expired {
		if candidate.ID == task.ID {
			t.Fatal("active capacity retry was expired while its launch lease was held")
		}
	}
	if err := leaseStore.Release(ctx, lease); err != nil {
		t.Fatalf("release capacity retry lease: %v", err)
	}
	if err := svc.MarkRuntimeStartBlocked(ctx, serializationAttempt); err != nil {
		t.Fatalf("mark newer serialization block: %v", err)
	}
	waiting, err = queries.ListASBCapacityWaitingTasks(ctx, db.ListASBCapacityWaitingTasksParams{
		RetrySeconds: 0,
		StaleSeconds: time.Hour.Seconds(),
	})
	if err != nil {
		t.Fatalf("list capacity waits after newer attempt: %v", err)
	}
	for _, candidate := range waiting {
		if candidate.ID == task.ID {
			t.Fatal("historical capacity attempt overrode the latest serialization attempt")
		}
	}
	expired, err = queries.ExpireStaleQueuedTasks(ctx, db.ExpireStaleQueuedTasksParams{
		TtlSecs:            1,
		AsbCapacityTtlSecs: 24 * time.Hour.Seconds(),
		MaxPerTick:         10,
	})
	if err != nil {
		t.Fatalf("expire queue after capacity wait ended: %v", err)
	}
	found := false
	for _, candidate := range expired {
		if candidate.ID == task.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("task with newer non-capacity attempt remained exempt from queue TTL")
	}
}

func TestASBCapacityWaiterRecoversAbandonedASBStartAttempt(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())
	taskID, _, _ := dispatchedCommentTaskFixture(t, ctx, pool)
	taskUUID := util.MustParseUUID(taskID)
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'queued',
		    dispatched_at = NULL,
		    created_at = now() - interval '3 hours'
		WHERE id = $1
	`, taskUUID); err != nil {
		t.Fatalf("reset abandoned ASB task: %v", err)
	}
	task, err := queries.GetAgentTask(ctx, taskUUID)
	if err != nil {
		t.Fatalf("load abandoned ASB task: %v", err)
	}
	attempt, err := svc.BeginRuntimeStartAttempt(
		ctx,
		task,
		SandboxBackendASB,
		RuntimeStartProtocolHTTPJSONV1,
	)
	if err != nil {
		t.Fatalf("begin abandoned ASB attempt: %v", err)
	}
	expired, err := queries.ExpireStaleQueuedTasks(ctx, db.ExpireStaleQueuedTasksParams{
		TtlSecs:            1,
		AsbCapacityTtlSecs: 24 * time.Hour.Seconds(),
		MaxPerTick:         10,
	})
	if err != nil {
		t.Fatalf("expire queue before stale ASB recovery: %v", err)
	}
	for _, candidate := range expired {
		if candidate.ID == task.ID {
			t.Fatal("first ASB start crash expired before stale-start recovery window")
		}
	}
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_runtime_start_attempt
		SET updated_at = now() - interval '4 minutes'
		WHERE id = $1
	`, attempt.ID); err != nil {
		t.Fatalf("age abandoned ASB attempt: %v", err)
	}

	waiting, err := queries.ListASBCapacityWaitingTasks(ctx, db.ListASBCapacityWaitingTasksParams{
		RetrySeconds: 5,
		StaleSeconds: (3 * time.Minute).Seconds(),
	})
	if err != nil {
		t.Fatalf("list abandoned ASB attempts: %v", err)
	}
	found := false
	for _, candidate := range waiting {
		if candidate.ID == task.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("stale ASB start attempt was not recovered after its launch lease expired")
	}
}

func TestASBCapacityWaitClosesAttemptWhenCancellationWins(t *testing.T) {
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
		t.Fatalf("reset cancellable ASB task: %v", err)
	}
	task, err := queries.GetAgentTask(ctx, taskUUID)
	if err != nil {
		t.Fatalf("load cancellable ASB task: %v", err)
	}
	attempt, err := svc.BeginRuntimeStartAttempt(ctx, task, SandboxBackendASB, RuntimeStartProtocolHTTPJSONV1)
	if err != nil {
		t.Fatalf("begin cancellable ASB attempt: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'cancelled', completed_at = now()
		WHERE id = $1
	`, task.ID); err != nil {
		t.Fatalf("cancel ASB task: %v", err)
	}
	waiting, err := svc.MarkRuntimeStartCapacityWaiting(ctx, attempt)
	if err != nil {
		t.Fatalf("close cancelled capacity attempt: %v", err)
	}
	if waiting {
		t.Fatal("cancelled task was recorded as capacity waiting")
	}
	got, err := queries.GetAgentTaskRuntimeStartAttempt(ctx, db.GetAgentTaskRuntimeStartAttemptParams{
		ID: attempt.ID, TaskID: task.ID, RuntimeID: task.RuntimeID,
	})
	if err != nil {
		t.Fatalf("load cancelled ASB attempt: %v", err)
	}
	if got.Status != "superseded" || got.LastStage != "task_terminal_before_capacity_wait" || !got.FinishedAt.Valid {
		t.Fatalf("cancelled ASB attempt = %+v", got)
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

func TestA2ATokenlessClaimFinalizesRuntimeStartAttemptAtHandlerBoundary(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())
	taskID, _, _ := dispatchedCommentTaskFixture(t, ctx, pool)
	taskUUID := util.MustParseUUID(taskID)
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue
		SET context = jsonb_build_object('multica_origin', 'a2a')
		WHERE id = $1
	`, taskUUID); err != nil {
		t.Fatalf("mark task as A2A: %v", err)
	}
	task, err := queries.GetAgentTask(ctx, taskUUID)
	if err != nil {
		t.Fatalf("load A2A task: %v", err)
	}
	attempt, err := svc.BeginRuntimeStartAttempt(ctx, task, SandboxBackendAliyunFC, RuntimeStartProtocolHTTPJSONV1)
	if err != nil {
		t.Fatalf("begin A2A attempt: %v", err)
	}

	// The launcher observes claims but must not infer completion merely from a
	// dispatched A2A task. Only the handler's post-payload transaction opts into
	// the durable tokenless A2A proof.
	if _, err := queries.FinalizeAgentTaskRuntimeStartAttemptForTask(ctx, db.FinalizeAgentTaskRuntimeStartAttemptForTaskParams{
		TaskID: task.ID, RuntimeID: task.RuntimeID, AllowTokenlessA2a: false,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("launcher observation before handler finalization = %v, want no rows", err)
	}
	before, err := queries.GetAgentTaskRuntimeStartAttempt(ctx, db.GetAgentTaskRuntimeStartAttemptParams{
		ID: attempt.ID, TaskID: task.ID, RuntimeID: task.RuntimeID,
	})
	if err != nil {
		t.Fatalf("load pre-finalization attempt: %v", err)
	}
	if before.Status != "starting" {
		t.Fatalf("launcher observation changed attempt to %q", before.Status)
	}

	if _, err := svc.FinalizeTaskClaimWithoutToken(ctx, task, nil, false); err != nil {
		t.Fatalf("finalize tokenless A2A claim: %v", err)
	}
	got, err := queries.GetAgentTaskRuntimeStartAttempt(ctx, db.GetAgentTaskRuntimeStartAttemptParams{
		ID: attempt.ID, TaskID: task.ID, RuntimeID: task.RuntimeID,
	})
	if err != nil {
		t.Fatalf("load finalized A2A attempt: %v", err)
	}
	if got.Status != "claimed" || got.LastStage != "claim_finalized" || !got.ClaimFinalizedAt.Valid {
		t.Fatalf("A2A attempt after tokenless claim = %+v", got)
	}
	var tokenCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM task_token WHERE task_id = $1`, task.ID).Scan(&tokenCount); err != nil {
		t.Fatalf("count A2A task tokens: %v", err)
	}
	if tokenCount != 0 {
		t.Fatalf("tokenless A2A claim persisted %d task tokens", tokenCount)
	}

	failure := NewRuntimeStartFailure(
		SandboxBackendAliyunFC,
		"FCE2B-RUNNER-CLAIM-TIMEOUT",
		"claim_wait",
		true,
		"Runner 未在规定时间内完成任务领取。",
		"late launcher timeout after tokenless A2A finalization",
	)
	gotTask, err := svc.FailTaskRuntimeStart(ctx, task.ID, task.RuntimeID, attempt.ID, failure)
	if err != nil {
		t.Fatalf("late A2A startup failure: %v", err)
	}
	if gotTask.Status != "dispatched" {
		t.Fatalf("late A2A startup failure changed task status to %q", gotTask.Status)
	}
	got, err = queries.GetAgentTaskRuntimeStartAttempt(ctx, db.GetAgentTaskRuntimeStartAttemptParams{
		ID: attempt.ID, TaskID: task.ID, RuntimeID: task.RuntimeID,
	})
	if err != nil {
		t.Fatalf("reload A2A attempt after late failure: %v", err)
	}
	if got.Status != "claimed" {
		t.Fatalf("late A2A startup failure changed attempt status to %q", got.Status)
	}
}

func TestTokenlessRuntimeStartFinalizationRequiresDurableA2AOrigin(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())
	taskID, _, _ := dispatchedCommentTaskFixture(t, ctx, pool)
	task, err := queries.GetAgentTask(ctx, util.MustParseUUID(taskID))
	if err != nil {
		t.Fatalf("load ordinary task: %v", err)
	}
	attempt, err := svc.BeginRuntimeStartAttempt(ctx, task, SandboxBackendAliyunFC, RuntimeStartProtocolHTTPJSONV1)
	if err != nil {
		t.Fatalf("begin ordinary attempt: %v", err)
	}

	if _, err := queries.FinalizeAgentTaskRuntimeStartAttemptForTask(ctx, db.FinalizeAgentTaskRuntimeStartAttemptForTaskParams{
		TaskID: task.ID, RuntimeID: task.RuntimeID, AllowTokenlessA2a: true,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("ordinary tokenless finalization = %v, want no rows", err)
	}
	got, err := queries.GetAgentTaskRuntimeStartAttempt(ctx, db.GetAgentTaskRuntimeStartAttemptParams{
		ID: attempt.ID, TaskID: task.ID, RuntimeID: task.RuntimeID,
	})
	if err != nil {
		t.Fatalf("load ordinary attempt: %v", err)
	}
	if got.Status != "starting" {
		t.Fatalf("ordinary tokenless finalization changed attempt to %q", got.Status)
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
