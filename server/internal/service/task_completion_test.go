package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const taskCompletionTestTarget = "router-target:v1:sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestBuildTaskCompletionUsesCanonicalFinalReply(t *testing.T) {
	rootID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	leafID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	rootAgentID := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	leafAgentID := pgtype.UUID{Bytes: [16]byte{4}, Valid: true}
	result, err := json.Marshal(map[string]any{"output": "第一行\\n第二行"})
	if err != nil {
		t.Fatal(err)
	}

	completion := buildTaskCompletion(
		taskCompletionTarget{
			RootTaskID:     rootID,
			AgentID:        rootAgentID,
			CallbackURL:    "/api/v1/dispatch-tasks/router-task-1/execution-result",
			TargetIdentity: taskCompletionTestTarget,
		},
		db.AgentTaskQueue{ID: leafID, AgentID: leafAgentID, SessionID: pgtype.Text{String: "session-1", Valid: true}},
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
	if completion.AgentID != rootAgentID {
		t.Fatalf("completion agent = %s, want root agent %s",
			util.UUIDToString(completion.AgentID),
			util.UUIDToString(rootAgentID),
		)
	}
}

func TestBuildTaskCompletionUsesProviderOutputOverLegacyResultMessage(t *testing.T) {
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

	if completion.ResultMessage != "agent execution summary" {
		t.Fatalf("result message = %q", completion.ResultMessage)
	}
}

func TestBuildTaskCompletionCarriesSuccessfulReplyDecision(t *testing.T) {
	result := []byte(`{"output":"可见回复","reply_decision":{"shouldReply":false,"reason":"echo"}}`)
	completion := buildTaskCompletion(
		taskCompletionTarget{RootTaskID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}},
		db.AgentTaskQueue{ID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}},
		"completed",
		result,
		"",
		"",
		"",
	)
	if completion.ReplyDecision == nil || completion.ReplyDecision.ShouldReply || completion.ReplyDecision.Reason != "echo" {
		t.Fatalf("reply decision = %#v", completion.ReplyDecision)
	}
}

func TestBuildTaskCompletionNormalizesRawReplyDecisionOutput(t *testing.T) {
	result := []byte("{\"output\":\"可见回复\\n\\n```multica-reply-decision\\n{\\\"shouldReply\\\":false,\\\"reason\\\":\\\"echo\\\"}\\n```\"}")
	completion := buildTaskCompletion(
		taskCompletionTarget{RootTaskID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}},
		db.AgentTaskQueue{ID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}},
		"completed",
		result,
		"",
		"",
		"",
	)
	if completion.ResultMessage != "可见回复" {
		t.Fatalf("result message = %q", completion.ResultMessage)
	}
	if completion.ReplyDecision == nil || completion.ReplyDecision.ShouldReply || completion.ReplyDecision.Reason != "echo" {
		t.Fatalf("reply decision = %#v", completion.ReplyDecision)
	}
}

func TestBuildTaskCompletionDoesNotCarryFailedReplyDecision(t *testing.T) {
	result := []byte(`{"output":"partial","reply_decision":{"shouldReply":false,"reason":"echo"}}`)
	completion := buildTaskCompletion(
		taskCompletionTarget{RootTaskID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}},
		db.AgentTaskQueue{ID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}},
		"failed",
		result,
		"partial",
		"failed",
		"agent_error.unknown",
	)
	if completion.ReplyDecision != nil {
		t.Fatalf("failed completion carried reply decision = %#v", completion.ReplyDecision)
	}
}

func TestBuildTaskCompletionRootUsesCanonicalOutputOverLastTaskMessage(t *testing.T) {
	result, err := json.Marshal(map[string]any{
		"output":         "我在联系人里搜索了一下，没有找到\"须莫v6\"这个人。\n\n目前联系人里只有\"须莫🥥\"，没有名为\"须莫v6\"的联系人。",
		"reply_decision": map[string]any{"shouldReply": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	lastTaskMessage := "名为\"须莫v6\"的联系人。\n\n```multica-reply-decision\n{\"shouldReply\":true}\n```"

	completion := buildTaskCompletion(
		taskCompletionTarget{RootTaskID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}},
		db.AgentTaskQueue{ID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}},
		"completed",
		result,
		lastTaskMessage,
		"",
		"",
	)

	want := "我在联系人里搜索了一下，没有找到\"须莫v6\"这个人。\n\n目前联系人里只有\"须莫🥥\"，没有名为\"须莫v6\"的联系人。"
	if completion.ResultMessage != want {
		t.Fatalf("result message = %q, want %q", completion.ResultMessage, want)
	}
	if completion.ReplyDecision == nil || !completion.ReplyDecision.ShouldReply {
		t.Fatalf("reply decision = %#v", completion.ReplyDecision)
	}
}

func TestBuildTaskCompletionCommentKeepsThreadReply(t *testing.T) {
	result := []byte(`{"output":"root task summary"}`)
	completion := buildTaskCompletion(
		taskCompletionTarget{
			RootTaskID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
			CommentID:  pgtype.UUID{Bytes: [16]byte{3}, Valid: true},
		},
		db.AgentTaskQueue{ID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}},
		"completed",
		result,
		"该评论对应的完整回复",
		"",
		"",
	)

	if completion.ResultMessage != "该评论对应的完整回复" {
		t.Fatalf("result message = %q", completion.ResultMessage)
	}
}

func TestBuildTaskCompletionCommentReplyOverridesRunSilence(t *testing.T) {
	result := []byte(`{"output":"任务已完成","reply_decision":{"shouldReply":false,"reason":"任务已完成，无新的用户输入"}}`)
	completion := buildTaskCompletion(
		taskCompletionTarget{
			RootTaskID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
			CommentID:  pgtype.UUID{Bytes: [16]byte{3}, Valid: true},
		},
		db.AgentTaskQueue{ID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}},
		"completed",
		result,
		"已经找到须莫v6，并把结果发回来了。",
		"",
		"",
	)

	if completion.ReplyDecision == nil || !completion.ReplyDecision.ShouldReply {
		t.Fatalf("comment completion reply decision = %#v", completion.ReplyDecision)
	}
}

func TestBuildTaskCompletionFailureKeepsLastReplyAndReason(t *testing.T) {
	completion := buildTaskCompletion(
		taskCompletionTarget{
			RootTaskID:     pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
			CallbackURL:    "/api/v1/dispatch-tasks/router-task-1/execution-result",
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

func TestBuildTaskCompletionFailureIgnoresLegacyResultMessage(t *testing.T) {
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

	if completion.ResultMessage != "legacy DB reply" {
		t.Fatalf("result message = %q", completion.ResultMessage)
	}
}

func TestBuildTaskCompletionDelegatedCommentPrefersThreadReplyOnFailure(t *testing.T) {
	completion := buildTaskCompletion(
		taskCompletionTarget{
			RootTaskID:     pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
			CallbackURL:    "/api/v1/dispatch-tasks/router-task-1/execution-result",
			TargetIdentity: taskCompletionTestTarget,
			CommentID:      pgtype.UUID{Bytes: [16]byte{4}, Valid: true},
		},
		db.AgentTaskQueue{
			ID:      pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
			AgentID: pgtype.UUID{Bytes: [16]byte{3}, Valid: true},
		},
		"failed",
		[]byte(`{"result_message":"本轮任务失败"}`),
		"该条评论对应的失败回复",
		"runtime lost",
		"runtime_offline",
	)

	if completion.ResultMessage != "该条评论对应的失败回复" {
		t.Fatalf("result message = %q", completion.ResultMessage)
	}
	if completion.RequestID != "multica-comment-terminal:01000000-0000-0000-0000-000000000000" {
		t.Fatalf("request id = %q", completion.RequestID)
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

func TestResolveFailedCompletionReplyIgnoresLegacyResultMessage(t *testing.T) {
	reader := &lastTaskReplyReaderStub{
		reply: pgtype.Text{String: "legacy DB reply", Valid: true},
	}
	taskID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}

	reply, err := resolveFailedCompletionReply(
		context.Background(),
		reader,
		taskID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "legacy DB reply" {
		t.Fatalf("reply = %q", reply)
	}
	if reader.calls != 1 {
		t.Fatalf("DB reply query calls = %d", reader.calls)
	}

	reply, err = resolveFailedCompletionReply(
		context.Background(),
		reader,
		taskID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "legacy DB reply" {
		t.Fatalf("fallback reply = %q", reply)
	}
	if reader.calls != 2 {
		t.Fatalf("DB reply query calls = %d", reader.calls)
	}
}

func TestRetryEligibleIgnoresLegacyResultMessage(t *testing.T) {
	task := db.AgentTaskQueue{
		Attempt:     0,
		MaxAttempts: 2,
		IssueID:     pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		Result:      []byte(`{"result_message":"已向用户说明任务超时"}`),
	}
	if !retryEligible("timeout", task) {
		t.Fatal("legacy DWS reply receipt must not change explicit failure retry semantics")
	}
}

func TestCompleteTaskEnqueuesRouterCompletionInTerminalTransaction(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	agentID := createClaimCapacityFixture(t, ctx, pool)
	firstEffectiveReplyAt := time.Date(2026, 8, 13, 2, 3, 4, 567000000, time.UTC)
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
	if _, err := pool.Exec(ctx, `
		INSERT INTO task_message (task_id, seq, type, content, created_at)
		VALUES
			($1, 1, 'thinking', 'working', $2),
			($1, 2, 'text', 'answer', $3)
	`, taskID, firstEffectiveReplyAt, firstEffectiveReplyAt.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}

	svc := NewTaskService(db.New(pool), pool, nil, events.New())
	if _, err := svc.CompleteTask(
		ctx,
		util.MustParseUUID(taskID),
		[]byte("{\"output\":\"agent execution summary\\n\\n```multica-reply-decision\\n{\\\"shouldReply\\\":false,\\\"reason\\\":\\\"echo\\\"}\\n```\"}"),
		"session-1",
		"",
		false,
		"",
	); err != nil {
		t.Fatal(err)
	}

	var persistedResult []byte
	if err := pool.QueryRow(ctx, `SELECT result FROM agent_task_queue WHERE id = $1`, taskID).Scan(&persistedResult); err != nil {
		t.Fatal(err)
	}
	var persistedPayload protocol.TaskCompletedPayload
	if err := json.Unmarshal(persistedResult, &persistedPayload); err != nil {
		t.Fatal(err)
	}
	if persistedPayload.Output != "agent execution summary" {
		t.Fatalf("persisted output = %q", persistedPayload.Output)
	}
	if persistedPayload.ReplyDecision == nil || persistedPayload.ReplyDecision.ShouldReply || persistedPayload.ReplyDecision.Reason != "echo" {
		t.Fatalf("persisted reply decision = %#v", persistedPayload.ReplyDecision)
	}

	var rootTaskID, terminalTaskID, requestID, status, message, targetIdentity string
	var executionSummary []byte
	if err := pool.QueryRow(ctx, `
		SELECT root_task_id, terminal_task_id, request_id, execution_status, result_message, target_identity, execution_summary
		FROM task_completion_outbox
		WHERE root_task_id = $1
	`, taskID).Scan(&rootTaskID, &terminalTaskID, &requestID, &status, &message, &targetIdentity, &executionSummary); err != nil {
		t.Fatal(err)
	}
	if rootTaskID != taskID || terminalTaskID != taskID ||
		requestID != "multica-terminal:"+taskID || status != "completed" ||
		message != "agent execution summary" || targetIdentity != taskCompletionTestTarget {
		t.Fatalf("completion = root:%s terminal:%s request:%s status:%s message:%q",
			rootTaskID, terminalTaskID, requestID, status, message)
	}
	var summary TaskExecutionSummary
	if err := json.Unmarshal(executionSummary, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.TaskID != taskID || summary.Status != "completed" || summary.MessageCount != 2 {
		t.Fatalf("execution summary = %#v", summary)
	}
	if summary.FirstEffectiveReplyAt == nil {
		t.Fatal("first effective reply at is nil")
	}
	if got, want := *summary.FirstEffectiveReplyAt, firstEffectiveReplyAt.Format(time.RFC3339Nano); got != want {
		t.Fatalf("first effective reply at = %s, want %s", got, want)
	}
	var frozen map[string]any
	if err := json.Unmarshal(executionSummary, &frozen); err != nil {
		t.Fatal(err)
	}
	decision, ok := frozen["_multica_reply_decision"].(map[string]any)
	if !ok || decision["shouldReply"] != false || decision["reason"] != "echo" {
		t.Fatalf("frozen reply decision = %#v", frozen["_multica_reply_decision"])
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
		RootTaskID:      util.MustParseUUID(taskID),
		TerminalTaskID:  pgtype.UUID{Bytes: [16]byte{99}, Valid: true},
		CallbackUrl:     "/api/v1/dispatch-tasks/router-task-cas/execution-result",
		TargetIdentity:  taskCompletionTestTarget,
		RequestID:       "multica-terminal:" + taskID,
		AgentID:         util.MustParseUUID(agentID),
		ExecutionStatus: "failed",
		FailureReason:   pgtype.Text{String: "stale_parent", Valid: true},
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
		false,
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
		false,
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
				_, err := svc.CompleteTask(ctx, taskID, []byte(`{"output":"done"}`), "", "", false, "")
				return err
			},
		},
		{
			name: "fail",
			run: func(ctx context.Context, svc *TaskService, taskID pgtype.UUID) error {
				_, err := svc.FailTask(ctx, taskID, "failed", "", "", "agent_error", false, "")
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
		false,
		"",
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
		false,
		"",
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

func TestRetryProducersPreserveDeferredBudgetAndReplay(t *testing.T) {
	for _, producer := range []string{"fail", "recovery", "reconciler"} {
		t.Run(producer, func(t *testing.T) {
			ctx := context.Background()
			pool := newTaskClaimRacePool(t)
			agentID := createClaimCapacityFixture(t, ctx, pool)
			status := "failed"
			if producer == "fail" {
				status = "running"
			}
			var parentID pgtype.UUID
			if err := pool.QueryRow(ctx, `UPDATE agent_task_queue SET status=$2, failure_reason='agent_error.provider_network',
				attempt=2,max_attempts=2,completed_at=now(),session_id='retry-session',work_dir='/tmp/retry-fixture',
				context=$3::jsonb WHERE id=(SELECT id FROM agent_task_queue WHERE agent_id=$1 ORDER BY created_at LIMIT 1) RETURNING id`,
				agentID, status, `{"completion_callback":{"url":"/api/v1/dispatch-tasks/retry-budget/execution-result","target":"`+taskCompletionTestTarget+`"}}`).Scan(&parentID); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE root_task_id=$1`, parentID)
			})
			wakeups := &safeWakeupRecorder{}
			svc := NewTaskService(db.New(pool), pool, nil, events.New(), wakeups)
			parent, err := svc.Queries.GetAgentTask(ctx, parentID)
			if err != nil {
				t.Fatal(err)
			}
			before := time.Now()
			switch producer {
			case "fail":
				_, err = svc.FailTask(ctx, parentID, "connection closed", "retry-session", "/tmp/retry-fixture", "agent_error.provider_network", false, "")
			case "recovery":
				_, err = svc.MaybeRetryFailedTask(ctx, parent)
			case "reconciler":
				_, err = svc.ReconcileTaskCompletions(ctx, taskCompletionTestTarget, 100)
			}
			if err != nil {
				t.Fatal(err)
			}
			child, err := svc.Queries.GetRetryChildByParent(ctx, parentID)
			if err != nil {
				t.Fatal(err)
			}
			if child.Status != "deferred" || child.Attempt != 3 || child.MaxAttempts != 3 || !child.FireAt.Valid || child.FireAt.Time.Before(before.Add(5*time.Second)) {
				t.Fatalf("retry lost budget/backoff: status=%s attempt=%d max=%d fire_at=%v", child.Status, child.Attempt, child.MaxAttempts, child.FireAt)
			}
			if child.SessionID.String != "retry-session" || child.WorkDir.String != "/tmp/retry-fixture" {
				t.Fatalf("retry lost resumable execution: %+v", child)
			}
			// Use the stale running snapshot for fail: recovery must re-read the row.
			replay, err := svc.MaybeRetryFailedTask(ctx, parent)
			if err != nil || replay == nil || replay.ID != child.ID || replay.FireAt != child.FireAt {
				t.Fatalf("deferred retry replay changed identity/backoff: %+v, %v", replay, err)
			}
			if wakeups.count() != 0 {
				t.Fatalf("deferred retry/replay woke the runtime %d times", wakeups.count())
			}
		})
	}
}

func TestRetryRecoveryUsesCurrentParentState(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	agentID := createClaimCapacityFixture(t, ctx, pool)
	var parentID pgtype.UUID
	if err := pool.QueryRow(ctx, `UPDATE agent_task_queue SET status='failed',failure_reason='timeout',attempt=1,max_attempts=2
		WHERE id=(SELECT id FROM agent_task_queue WHERE agent_id=$1 ORDER BY created_at LIMIT 1) RETURNING id`, agentID).Scan(&parentID); err != nil {
		t.Fatal(err)
	}
	svc := NewTaskService(db.New(pool), pool, nil, events.New())
	stale, err := svc.Queries.GetAgentTask(ctx, parentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status='cancelled' WHERE id=$1`, parentID); err != nil {
		t.Fatal(err)
	}
	child, err := svc.MaybeRetryFailedTask(ctx, stale)
	if err != nil || child != nil {
		t.Fatalf("stale failed snapshot retried a stopped parent: %+v, %v", child, err)
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

	wakeups := &safeWakeupRecorder{}
	svc := NewTaskService(db.New(pool), pool, nil, events.New(), wakeups)
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
	if wakeups.count() != 1 {
		t.Fatalf("retry wakeups = %d, want one", wakeups.count())
	}
	replayed, err := svc.MaybeRetryFailedTask(ctx, parent)
	if err != nil || replayed == nil || util.UUIDToString(replayed.ID) != childTaskID {
		t.Fatalf("retry replay = %+v, %v; want existing child %s", replayed, err, childTaskID)
	}
	if _, err := svc.ReconcileTaskCompletions(ctx, taskCompletionTestTarget, 100); err != nil {
		t.Fatal(err)
	}
	if wakeups.count() != 1 {
		t.Fatalf("replay emitted another wakeup: %d", wakeups.count())
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
		RootTaskID:       rootID,
		TerminalTaskID:   pgtype.UUID{Bytes: [16]byte{11}, Valid: true},
		CallbackUrl:      "/api/v1/dispatch-tasks/router-task-conflict/execution-result",
		TargetIdentity:   taskCompletionTestTarget,
		RequestID:        "multica-terminal:conflict",
		AgentID:          pgtype.UUID{Bytes: [16]byte{12}, Valid: true},
		ExecutionStatus:  "completed",
		ResultMessage:    "done",
		ExecutionSummary: []byte(`{"task_id":"task-1","status":"completed"}`),
	}
	if _, err := queries.EnqueueTaskCompletion(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.EnqueueTaskCompletion(ctx, first); err != nil {
		t.Fatalf("exact completion replay failed: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE root_task_id = $1`, rootID)
	})

	summaryConflict := first
	summaryConflict.ExecutionSummary = []byte(`{"task_id":"task-1","status":"failed"}`)
	if _, err := queries.EnqueueTaskCompletion(ctx, summaryConflict); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("conflicting execution summary error = %v, want pgx.ErrNoRows", err)
	}

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
	if status != "canceled" || reason != "cancelled" || errMessage != "task cancelled" {
		t.Fatalf("completion = status:%s reason:%s error:%q", status, reason, errMessage)
	}
}

func TestEnqueueSynchronousSilenceCompletesWithoutFailureStamp(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	svc := NewTaskService(queries, pool, nil, events.New())
	agentID := pgtype.UUID{Bytes: [16]byte{22}, Valid: true}
	callback := "/api/v1/dispatch-tasks/router-coord-silence/execution-result"
	t.Cleanup(func() {
		pool.Exec(context.Background(), `
			DELETE FROM task_completion_outbox
			WHERE request_id = 'multica-terminal:sync-silence:router-coord-silence'
		`)
	})
	if err := svc.EnqueueSynchronousSilence(ctx, callback, taskCompletionTestTarget, agentID); err != nil {
		t.Fatal(err)
	}
	var status, result string
	var summary []byte
	if err := pool.QueryRow(ctx, `
		SELECT execution_status, result_message, execution_summary
		FROM task_completion_outbox
		WHERE request_id = 'multica-terminal:sync-silence:router-coord-silence'
	`).Scan(&status, &result, &summary); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || result != "" {
		t.Fatalf("silence must complete with empty IM, status=%q result=%q", status, result)
	}
	var frozen map[string]any
	if err := json.Unmarshal(summary, &frozen); err != nil {
		t.Fatal(err)
	}
	decision, _ := frozen["_multica_reply_decision"].(map[string]any)
	if decision == nil {
		t.Fatalf("missing reply decision: %s", summary)
	}
	if decision["shouldReply"] != false {
		t.Fatalf("shouldReply=%v want false", decision["shouldReply"])
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

func TestEnqueueCoordinatorFailureReplyRemainsFailedAndIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	svc := NewTaskService(db.New(pool), pool, nil, events.New())
	agentID := pgtype.UUID{Bytes: [16]byte{22}, Valid: true}
	callback := "/api/v1/dispatch-tasks/coordinator-failure-reply/execution-result"
	requestID := "multica-terminal:sync:coordinator-failure-reply"
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM task_completion_outbox WHERE request_id=$1", requestID)
	})
	for range 2 {
		if err := svc.EnqueueSynchronousTaskFailureReply(ctx, callback, taskCompletionTestTarget, agentID, "private diagnostic", "coordinator_job_failed", "抱歉，这次没能处理好。"); err != nil {
			t.Fatal(err)
		}
	}
	var status, text, diagnostic string
	var count int
	if err := pool.QueryRow(ctx, "SELECT execution_status,result_message,error,count(*) OVER () FROM task_completion_outbox WHERE request_id=$1", requestID).Scan(&status, &text, &diagnostic, &count); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || text != "抱歉，这次没能处理好。" || diagnostic != "private diagnostic" || count != 1 {
		t.Fatalf("unexpected failure receipt: status=%s text=%q diagnostic=%q count=%d", status, text, diagnostic, count)
	}
}
