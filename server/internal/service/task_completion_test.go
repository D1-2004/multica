package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const taskCompletionTestTarget = "router-target:v1:sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestBuildTaskCompletionUsesCanonicalFinalReply(t *testing.T) {
	rootID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	leafID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	agentID := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	result, err := json.Marshal(map[string]any{"output": "第一行\\n第二行"})
	if err != nil {
		t.Fatal(err)
	}

	completion := buildTaskCompletion(
		taskCompletionTarget{
			RootTaskID:    rootID,
			CallbackURL: "/api/v1/dispatch-tasks/router-task-1/execution-result",
			TargetIdentity: taskCompletionTestTarget,
		},
		db.AgentTaskQueue{ID: leafID, AgentID: agentID, SessionID: pgtype.Text{String: "session-1", Valid: true}},
		"completed",
		result,
		"",
		"",
		"",
	)

	if completion.RequestID != "multica-terminal:01000000-0000-0000-0000-000000000000" {
		t.Fatalf("request id = %q", completion.RequestID)
	}
	if completion.ResultMessage != "第一行\n第二行" {
		t.Fatalf("result message = %q", completion.ResultMessage)
	}
	if completion.ExecutionStatus != "completed" ||
		completion.CallbackURL != "/api/v1/dispatch-tasks/router-task-1/execution-result" {
		t.Fatalf("completion = %#v", completion)
	}
}

func TestBuildTaskCompletionPrefersExplicitResultMessage(t *testing.T) {
	result, err := json.Marshal(map[string]any{
		"output":         "agent execution summary",
		"result_message": `在的！有什么需要帮忙的吗？ "原文"`,
	})
	if err != nil {
		t.Fatal(err)
	}

	completion := buildTaskCompletion(
		taskCompletionTarget{
			RootTaskID:     pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
			CallbackURL:    "/api/v1/dispatch-tasks/router-task-1/execution-result",
			TargetIdentity: taskCompletionTestTarget,
		},
		db.AgentTaskQueue{
			ID:      pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
			AgentID: pgtype.UUID{Bytes: [16]byte{3}, Valid: true},
		},
		"completed",
		result,
		"",
		"",
		"",
	)

	if completion.ResultMessage != `在的！有什么需要帮忙的吗？ "原文"` {
		t.Fatalf("result message = %q", completion.ResultMessage)
	}
}

func TestBuildTaskCompletionFailureKeepsLastReplyAndReason(t *testing.T) {
	completion := buildTaskCompletion(
		taskCompletionTarget{
			RootTaskID:    pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
			CallbackURL:   "/api/v1/dispatch-tasks/router-task-1/execution-result",
			TargetIdentity: taskCompletionTestTarget,
		},
		db.AgentTaskQueue{ID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}, AgentID: pgtype.UUID{Bytes: [16]byte{3}, Valid: true}},
		"failed",
		nil,
		"last partial reply",
		"runtime lost",
		"runtime_offline",
	)

	if completion.ResultMessage != "last partial reply" ||
		completion.Error != "runtime lost" ||
		completion.FailureReason != "runtime_offline" {
		t.Fatalf("completion = %#v", completion)
	}
}

func TestBuildTaskCompletionFailurePrefersExplicitResultMessage(t *testing.T) {
	completion := buildTaskCompletion(
		taskCompletionTarget{
			RootTaskID:     pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
			CallbackURL:    "/api/v1/dispatch-tasks/router-task-1/execution-result",
			TargetIdentity: taskCompletionTestTarget,
		},
		db.AgentTaskQueue{
			ID:      pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
			AgentID: pgtype.UUID{Bytes: [16]byte{3}, Valid: true},
		},
		"failed",
		[]byte(`{"result_message":"已向用户说明任务失败"}`),
		"legacy DB reply",
		"runtime lost",
		"runtime_offline",
	)

	if completion.ResultMessage != "已向用户说明任务失败" {
		t.Fatalf("result message = %q", completion.ResultMessage)
	}
}

type lastTaskReplyReaderStub struct {
	reply pgtype.Text
	err   error
	calls int
}

func (s *lastTaskReplyReaderStub) GetLastTaskReplyText(
	context.Context,
	pgtype.UUID,
) (pgtype.Text, error) {
	s.calls++
	return s.reply, s.err
}

func TestResolveFailedCompletionReplySkipsDBForExplicitResultMessage(t *testing.T) {
	reader := &lastTaskReplyReaderStub{
		reply: pgtype.Text{String: "legacy DB reply", Valid: true},
	}
	taskID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}

	reply, err := resolveFailedCompletionReply(
		context.Background(),
		reader,
		taskID,
		[]byte(`{"result_message":"已向用户说明任务失败"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "已向用户说明任务失败" {
		t.Fatalf("reply = %q", reply)
	}
	if reader.calls != 0 {
		t.Fatalf("DB reply query calls = %d", reader.calls)
	}

	reply, err = resolveFailedCompletionReply(
		context.Background(),
		reader,
		taskID,
		[]byte(`{"result_message":""}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "legacy DB reply" {
		t.Fatalf("fallback reply = %q", reply)
	}
	if reader.calls != 1 {
		t.Fatalf("DB reply query calls = %d", reader.calls)
	}
}

func TestRetryEligibleForReportedFailureRejectsAlreadyRepliedTask(t *testing.T) {
	task := db.AgentTaskQueue{
		Attempt:     0,
		MaxAttempts: 2,
		IssueID:     pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
	}
	if retryEligibleForReportedFailure("timeout", task, "已向用户说明任务超时") {
		t.Fatal("already-replied task must not auto-retry")
	}
	if !retryEligibleForReportedFailure("timeout", task, "") {
		t.Fatal("failure without explicit reply should keep existing retry semantics")
	}

	task.Result = []byte(`{"result_message":"已向用户说明任务超时"}`)
	if retryEligible("timeout", task) {
		t.Fatal("persisted explicit reply must prevent later reconciliation from auto-retrying")
	}
}

func TestCompleteTaskEnqueuesRouterCompletionInTerminalTransaction(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	agentID := createClaimCapacityFixture(t, ctx, pool)
	var taskID string
	if err := pool.QueryRow(ctx, `
		UPDATE agent_task_queue
		SET status = 'running',
		    started_at = now(),
		    context = '{"completion_callback":{"url":"/api/v1/dispatch-tasks/router-task-1/execution-result","target":"`+taskCompletionTestTarget+`"}}'::jsonb
		WHERE id = (
		    SELECT id FROM agent_task_queue WHERE agent_id = $1 ORDER BY created_at LIMIT 1
		)
		RETURNING id
	`, agentID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE root_task_id = $1`, taskID)
	})

	svc := NewTaskService(db.New(pool), pool, nil, events.New())
	if _, err := svc.CompleteTask(
		ctx,
		util.MustParseUUID(taskID),
		[]byte(`{"output":"agent execution summary","result_message":"第一行\\n第二行"}`),
		"session-1",
		"",
	); err != nil {
		t.Fatal(err)
	}

	var rootTaskID, terminalTaskID, requestID, status, message, targetIdentity string
	if err := pool.QueryRow(ctx, `
		SELECT root_task_id, terminal_task_id, request_id, execution_status, result_message, target_identity
		FROM task_completion_outbox
		WHERE root_task_id = $1
	`, taskID).Scan(&rootTaskID, &terminalTaskID, &requestID, &status, &message, &targetIdentity); err != nil {
		t.Fatal(err)
	}
	if rootTaskID != taskID || terminalTaskID != taskID ||
		requestID != "multica-terminal:"+taskID || status != "completed" ||
		message != "第一行\n第二行" || targetIdentity != taskCompletionTestTarget {
		t.Fatalf("completion = root:%s terminal:%s request:%s status:%s message:%q",
			rootTaskID, terminalTaskID, requestID, status, message)
	}
}

func TestFailTaskWithResultMessageSkipsRetryAndEnqueuesExplicitReply(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	agentID := createClaimCapacityFixture(t, ctx, pool)
	var taskID string
	if err := pool.QueryRow(ctx, `
		UPDATE agent_task_queue
		SET status = 'running',
		    started_at = now(),
		    attempt = 0,
		    max_attempts = 2,
		    context = '{"completion_callback":{"url":"/api/v1/dispatch-tasks/router-task-failed-reply/execution-result","target":"`+taskCompletionTestTarget+`"}}'::jsonb
		WHERE id = (
		    SELECT id FROM agent_task_queue WHERE agent_id = $1 ORDER BY created_at LIMIT 1
		)
		RETURNING id
	`, agentID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE root_task_id = $1`, taskID)
	})

	svc := NewTaskService(db.New(pool), pool, nil, events.New())
	if _, err := svc.FailTaskWithResultMessage(
		ctx,
		util.MustParseUUID(taskID),
		"runtime timed out",
		"已向用户说明任务超时",
		"session-1",
		"",
		"timeout",
	); err != nil {
		t.Fatal(err)
	}

	var childCount int
	var status, resultMessage, persistedResultMessage string
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM agent_task_queue WHERE parent_task_id = $1),
			outbox.execution_status,
			outbox.result_message,
			task.result->>'result_message'
		FROM agent_task_queue task
		JOIN task_completion_outbox outbox ON outbox.root_task_id = task.id
		WHERE task.id = $1
	`, taskID).Scan(&childCount, &status, &resultMessage, &persistedResultMessage); err != nil {
		t.Fatal(err)
	}
	if childCount != 0 {
		t.Fatalf("auto-retry child count = %d", childCount)
	}
	if status != "failed" || resultMessage != "已向用户说明任务超时" ||
		persistedResultMessage != "已向用户说明任务超时" {
		t.Fatalf(
			"completion = status:%s result_message:%q persisted:%q",
			status,
			resultMessage,
			persistedResultMessage,
		)
	}
}

func TestCompleteTaskDoesNotAckOutboxConflictAndCanRetry(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	agentID := createClaimCapacityFixture(t, ctx, pool)
	var taskID string
	if err := pool.QueryRow(ctx, `
		UPDATE agent_task_queue
		SET status = 'running',
		    started_at = now(),
		    context = '{"completion_callback":{"url":"/api/v1/dispatch-tasks/router-task-cas/execution-result","target":"`+taskCompletionTestTarget+`"}}'::jsonb
		WHERE id = (
		    SELECT id FROM agent_task_queue WHERE agent_id = $1 ORDER BY created_at LIMIT 1
		)
		RETURNING id
	`, agentID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	queries := db.New(pool)
	conflict, err := queries.EnqueueTaskCompletion(ctx, db.EnqueueTaskCompletionParams{
		RootTaskID:       util.MustParseUUID(taskID),
		TerminalTaskID:   pgtype.UUID{Bytes: [16]byte{99}, Valid: true},
		CallbackUrl:      "/api/v1/dispatch-tasks/router-task-cas/execution-result",
		TargetIdentity:   taskCompletionTestTarget,
		RequestID:        "multica-terminal:" + taskID,
		AgentID:          util.MustParseUUID(agentID),
		ExecutionStatus:  "failed",
		FailureReason:    pgtype.Text{String: "stale_parent", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE root_task_id = $1`, taskID)
	})

	svc := NewTaskService(queries, pool, nil, events.New())
	if _, err := svc.CompleteTask(
		ctx,
		util.MustParseUUID(taskID),
		[]byte(`{"output":"最终回复"}`),
		"session-1",
		"",
	); !errors.Is(err, ErrTaskCompletionConflict) {
		t.Fatalf("complete conflict error = %v", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `
		SELECT status FROM agent_task_queue WHERE id = $1
	`, taskID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Fatalf("task status after rolled-back conflict = %q", status)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM task_completion_outbox WHERE id = $1`, conflict.ID); err != nil {
		t.Fatal(err)
	}
	completed, err := svc.CompleteTask(
		ctx,
		util.MustParseUUID(taskID),
		[]byte(`{"output":"最终回复"}`),
		"session-1",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "completed" {
		t.Fatalf("retried completion status = %q", completed.Status)
	}
}

func TestTerminalTaskCASDoesNotAcknowledgeNonTerminalTask(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(context.Context, *TaskService, pgtype.UUID) error
	}{
		{
			name: "complete",
			run: func(ctx context.Context, svc *TaskService, taskID pgtype.UUID) error {
				_, err := svc.CompleteTask(ctx, taskID, []byte(`{"output":"done"}`), "", "")
				return err
			},
		},
		{
			name: "fail",
			run: func(ctx context.Context, svc *TaskService, taskID pgtype.UUID) error {
				_, err := svc.FailTask(ctx, taskID, "failed", "", "", "agent_error")
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := newTaskClaimRacePool(t)
			agentID := createClaimCapacityFixture(t, ctx, pool)
			var taskID string
			if err := pool.QueryRow(ctx, `
				SELECT id
				FROM agent_task_queue
				WHERE agent_id = $1 AND status = 'queued'
				ORDER BY created_at
				LIMIT 1
			`, agentID).Scan(&taskID); err != nil {
				t.Fatal(err)
			}
			svc := NewTaskService(db.New(pool), pool, nil, events.New())
			if err := tc.run(ctx, svc, util.MustParseUUID(taskID)); err == nil {
				t.Fatal("non-terminal task CAS was acknowledged as an idempotent terminal result")
			}
			var status string
			if err := pool.QueryRow(ctx, `
				SELECT status FROM agent_task_queue WHERE id = $1
			`, taskID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "queued" {
				t.Fatalf("task status = %q, want queued", status)
			}
		})
	}
}

func TestFailTaskDefersCompletionUntilRetryChainTerminates(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	agentID := createClaimCapacityFixture(t, ctx, pool)
	var rootTaskID string
	if err := pool.QueryRow(ctx, `
		UPDATE agent_task_queue
		SET status = 'running',
		    started_at = now(),
		    attempt = 1,
		    max_attempts = 2,
		    context = '{"completion_callback":{"url":"/api/v1/dispatch-tasks/router-task-2/execution-result","target":"`+taskCompletionTestTarget+`"}}'::jsonb
		WHERE id = (
		    SELECT id FROM agent_task_queue WHERE agent_id = $1 ORDER BY created_at LIMIT 1
		)
		RETURNING id
	`, agentID).Scan(&rootTaskID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE root_task_id = $1`, rootTaskID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO task_message (task_id, seq, type, content)
		VALUES ($1, 1, 'text', '重试前的最后回复')
	`, rootTaskID); err != nil {
		t.Fatal(err)
	}

	svc := NewTaskService(db.New(pool), pool, nil, events.New())
	if _, err := svc.FailTask(
		ctx,
		util.MustParseUUID(rootTaskID),
		"runtime went offline",
		"session-1",
		"",
		"runtime_offline",
	); err != nil {
		t.Fatal(err)
	}
	var outboxCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM task_completion_outbox WHERE root_task_id = $1
	`, rootTaskID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 0 {
		t.Fatalf("retryable parent produced %d completion rows", outboxCount)
	}

	var childTaskID, callbackURL string
	if err := pool.QueryRow(ctx, `
		UPDATE agent_task_queue
		SET status = 'running', started_at = now()
		WHERE parent_task_id = $1
		RETURNING id, context #>> '{completion_callback,url}'
	`, rootTaskID).Scan(&childTaskID, &callbackURL); err != nil {
		t.Fatal(err)
	}
	if callbackURL != "/api/v1/dispatch-tasks/router-task-2/execution-result" {
		t.Fatalf("retry callback = %q", callbackURL)
	}
	if _, err := svc.FailTask(
		ctx,
		util.MustParseUUID(childTaskID),
		"agent failed",
		"session-2",
		"",
		"agent_error",
	); err != nil {
		t.Fatal(err)
	}

	var terminalTaskID, status, reason, resultMessage string
	if err := pool.QueryRow(ctx, `
		SELECT terminal_task_id, execution_status, failure_reason, result_message
		FROM task_completion_outbox
		WHERE root_task_id = $1
	`, rootTaskID).Scan(&terminalTaskID, &status, &reason, &resultMessage); err != nil {
		t.Fatal(err)
	}
	if terminalTaskID != childTaskID || status != "failed" || reason != "agent_error" ||
		resultMessage != "重试前的最后回复" {
		t.Fatalf("completion = terminal:%s status:%s reason:%s message:%q",
			terminalTaskID, status, reason, resultMessage)
	}
}

func TestFailedTaskFinalizationSerializesRetryAndReconciler(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	agentID := createClaimCapacityFixture(t, ctx, pool)
	var rootTaskID string
	if err := pool.QueryRow(ctx, `
		UPDATE agent_task_queue
		SET status = 'failed',
		    completed_at = now(),
		    error = 'runtime timeout',
		    failure_reason = 'timeout',
		    attempt = 1,
		    max_attempts = 2,
		    context = '{"completion_callback":{"url":"/api/v1/dispatch-tasks/router-task-race/execution-result","target":"`+taskCompletionTestTarget+`"}}'::jsonb
		WHERE id = (
		    SELECT id FROM agent_task_queue WHERE agent_id = $1 ORDER BY created_at LIMIT 1
		)
		RETURNING id
	`, agentID).Scan(&rootTaskID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE root_task_id = $1`, rootTaskID)
	})

	svc := NewTaskService(db.New(pool), pool, nil, events.New())
	parent, err := svc.Queries.GetAgentTask(ctx, util.MustParseUUID(rootTaskID))
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 4)
	for index := 0; index < 4; index++ {
		wait.Add(1)
		go func(reconcile bool) {
			defer wait.Done()
			if reconcile {
				_, reconcileErr := svc.ReconcileTaskCompletions(ctx, taskCompletionTestTarget, 100)
				errorsSeen <- reconcileErr
				return
			}
			_, retryErr := svc.MaybeRetryFailedTask(ctx, parent)
			errorsSeen <- retryErr
		}(index%2 == 0)
	}
	wait.Wait()
	close(errorsSeen)
	for finalizeErr := range errorsSeen {
		if finalizeErr != nil {
			t.Fatal(finalizeErr)
		}
	}

	var childTaskID string
	var childCount, outboxCount int
	if err := pool.QueryRow(ctx, `
		SELECT task.id::text, (
			SELECT count(*) FROM agent_task_queue WHERE parent_task_id = $1
		)
		FROM agent_task_queue task
		WHERE task.parent_task_id = $1
		ORDER BY task.created_at
		LIMIT 1
	`, rootTaskID).Scan(&childTaskID, &childCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM task_completion_outbox WHERE root_task_id = $1
	`, rootTaskID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if childCount != 1 || outboxCount != 0 {
		t.Fatalf("retry decision created children=%d outbox=%d", childCount, outboxCount)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'failed',
		    completed_at = now(),
		    error = 'agent failed',
		    failure_reason = 'agent_error'
		WHERE id = $1
	`, childTaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReconcileTaskCompletions(ctx, taskCompletionTestTarget, 100); err != nil {
		t.Fatal(err)
	}
	var terminalTaskID string
	if err := pool.QueryRow(ctx, `
		SELECT completion.terminal_task_id::text, (
			SELECT count(*) FROM task_completion_outbox WHERE root_task_id = $1
		)
		FROM task_completion_outbox completion
		WHERE completion.root_task_id = $1
		LIMIT 1
	`, rootTaskID).Scan(&terminalTaskID, &outboxCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 1 || terminalTaskID != childTaskID {
		t.Fatalf("final completion count=%d terminal=%s child=%s",
			outboxCount, terminalTaskID, childTaskID)
	}
}

func TestTaskCompletionOutboxRejectsConflictingTerminalResult(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	rootID := pgtype.UUID{Bytes: [16]byte{10}, Valid: true}
	first := db.EnqueueTaskCompletionParams{
		RootTaskID:      rootID,
		TerminalTaskID:  pgtype.UUID{Bytes: [16]byte{11}, Valid: true},
		CallbackUrl:     "/api/v1/dispatch-tasks/router-task-conflict/execution-result",
		TargetIdentity:  taskCompletionTestTarget,
		RequestID:       "multica-terminal:conflict",
		AgentID:         pgtype.UUID{Bytes: [16]byte{12}, Valid: true},
		ExecutionStatus: "completed",
		ResultMessage:   "done",
	}
	if _, err := queries.EnqueueTaskCompletion(ctx, first); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE root_task_id = $1`, rootID)
	})

	conflict := first
	conflict.TerminalTaskID = pgtype.UUID{Bytes: [16]byte{13}, Valid: true}
	conflict.ExecutionStatus = "failed"
	conflict.ResultMessage = ""
	if _, err := queries.EnqueueTaskCompletion(ctx, conflict); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("conflicting completion error = %v, want pgx.ErrNoRows", err)
	}
}

func TestReconcileTaskCompletionsRepairsNonRetryableSweeperTerminalTask(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	agentID := createClaimCapacityFixture(t, ctx, pool)
	var taskID string
	if err := pool.QueryRow(ctx, `
		UPDATE agent_task_queue
		SET status = 'failed',
		    completed_at = now(),
		    error = 'runtime sweep terminal failure',
		    failure_reason = 'agent_error',
		    context = '{"completion_callback":{"url":"/api/v1/dispatch-tasks/router-task-sweep/execution-result","target":"`+taskCompletionTestTarget+`"}}'::jsonb
		WHERE id = (
		    SELECT id FROM agent_task_queue WHERE agent_id = $1 ORDER BY created_at LIMIT 1
		)
		RETURNING id
	`, agentID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE root_task_id = $1`, taskID)
	})

	svc := NewTaskService(db.New(pool), pool, nil, events.New())
	count, err := svc.ReconcileTaskCompletions(ctx, taskCompletionTestTarget, 100)
	if err != nil {
		t.Fatal(err)
	}
	if count < 1 {
		t.Fatalf("reconciled = %d", count)
	}
	var status, reason, callback string
	if err := pool.QueryRow(ctx, `
		SELECT execution_status, failure_reason, callback_url
		FROM task_completion_outbox
		WHERE root_task_id = $1
	`, taskID).Scan(&status, &reason, &callback); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || reason != "agent_error" ||
		callback != "/api/v1/dispatch-tasks/router-task-sweep/execution-result" {
		t.Fatalf("completion = status:%s reason:%s callback:%s", status, reason, callback)
	}
}

func TestReconcileTaskCompletionsRepairsCancelledTask(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	agentID := createClaimCapacityFixture(t, ctx, pool)
	var taskID string
	if err := pool.QueryRow(ctx, `
		UPDATE agent_task_queue
		SET status = 'cancelled',
		    completed_at = now(),
		    context = '{"completion_callback":{"url":"/api/v1/dispatch-tasks/router-task-cancelled/execution-result","target":"`+taskCompletionTestTarget+`"}}'::jsonb
		WHERE id = (
		    SELECT id FROM agent_task_queue WHERE agent_id = $1 ORDER BY created_at LIMIT 1
		)
		RETURNING id
	`, agentID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	// The database trigger is the primary cancellation guarantee. Removing its
	// row here simulates an externally damaged/missing outbox entry and keeps
	// this test focused on the reconciler's repair fallback.
	if _, err := pool.Exec(ctx, `
		DELETE FROM task_completion_outbox WHERE root_task_id = $1
	`, taskID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE root_task_id = $1`, taskID)
	})

	svc := NewTaskService(db.New(pool), pool, nil, events.New())
	count, err := svc.ReconcileTaskCompletions(ctx, taskCompletionTestTarget, 100)
	if err != nil {
		t.Fatal(err)
	}
	if count < 1 {
		t.Fatalf("reconciled = %d", count)
	}
	var status, reason, errMessage string
	if err := pool.QueryRow(ctx, `
		SELECT execution_status, failure_reason, error
		FROM task_completion_outbox
		WHERE root_task_id = $1
	`, taskID).Scan(&status, &reason, &errMessage); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || reason != "cancelled" || errMessage != "task cancelled" {
		t.Fatalf("completion = status:%s reason:%s error:%q", status, reason, errMessage)
	}
}

func TestEnqueueSynchronousTaskCompletionDurablyClosesNeedsBinding(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())
	agentID := pgtype.UUID{Bytes: [16]byte{21}, Valid: true}
	callback := "/api/v1/dispatch-tasks/router-needs-binding/execution-result"

	if err := svc.EnqueueSynchronousTaskCompletion(
		ctx,
		callback,
		taskCompletionTestTarget,
		agentID,
		"dingtalk account binding required",
		"needs_binding",
	); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnqueueSynchronousTaskCompletion(
		ctx,
		callback,
		taskCompletionTestTarget,
		agentID,
		"dingtalk account binding required",
		"needs_binding",
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `
			DELETE FROM task_completion_outbox
			WHERE request_id = 'multica-terminal:sync:router-needs-binding'
		`)
	})

	var count int
	var rootTaskID, terminalTaskID pgtype.UUID
	var status, reason string
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM task_completion_outbox
		WHERE request_id = 'multica-terminal:sync:router-needs-binding'
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT root_task_id, terminal_task_id, execution_status, failure_reason
		FROM task_completion_outbox
		WHERE request_id = 'multica-terminal:sync:router-needs-binding'
	`).Scan(&rootTaskID, &terminalTaskID, &status, &reason); err != nil {
		t.Fatal(err)
	}
	if count != 1 || rootTaskID.Valid || terminalTaskID.Valid ||
		status != "failed" || reason != "needs_binding" {
		t.Fatalf("count=%d root=%v terminal=%v status=%s reason=%s",
			count, rootTaskID, terminalTaskID, status, reason)
	}
}
