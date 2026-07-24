package service

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type cancelledCompletionFixture struct {
	pool        *pgxpool.Pool
	queries     *db.Queries
	agentID     string
	runtimeID   string
	workspaceID string
	userID      string
	taskIDs     []string
	issueIDs    []string
}

func newCancelledCompletionFixture(t *testing.T) cancelledCompletionFixture {
	t.Helper()
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	agentID := createClaimCapacityFixture(t, ctx, pool)
	fixture := cancelledCompletionFixture{
		pool:    pool,
		queries: db.New(pool),
		agentID: agentID,
	}
	if err := pool.QueryRow(ctx, `
		SELECT workspace_id, owner_id, runtime_id
		FROM agent
		WHERE id = $1
	`, agentID).Scan(&fixture.workspaceID, &fixture.userID, &fixture.runtimeID); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `
		SELECT id, issue_id
		FROM agent_task_queue
		WHERE agent_id = $1
		ORDER BY created_at, id
	`, agentID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var taskID, issueID string
		if err := rows.Scan(&taskID, &issueID); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		fixture.taskIDs = append(fixture.taskIDs, taskID)
		fixture.issueIDs = append(fixture.issueIDs, issueID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if len(fixture.taskIDs) < 2 {
		t.Fatalf("fixture task count = %d, want at least 2", len(fixture.taskIDs))
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `
			DELETE FROM task_completion_outbox WHERE agent_id = $1
		`, agentID)
	})
	return fixture
}

func cancelledCompletionCallback(taskID string) string {
	return "/api/v1/dispatch-tasks/cancel-" + taskID + "/execution-result"
}

func prepareRemoteCancellationTask(
	t *testing.T,
	fixture cancelledCompletionFixture,
	taskID string,
	status string,
) {
	t.Helper()
	if _, err := fixture.pool.Exec(context.Background(), `
		UPDATE agent_task_queue
		SET status = $2,
		    started_at = CASE WHEN $2 = 'running' THEN now() ELSE started_at END,
		    context = jsonb_build_object(
		        'completion_callback',
		        jsonb_build_object(
		            'url', $3::text,
		            'target', $4::text
		        )
		    )
		WHERE id = $1
	`, taskID, status, cancelledCompletionCallback(taskID), taskCompletionTestTarget); err != nil {
		t.Fatal(err)
	}
}

func assertCancelledCompletion(
	t *testing.T,
	fixture cancelledCompletionFixture,
	rootTaskID string,
	terminalTaskID string,
	wantReply string,
) {
	t.Helper()
	var rootID, terminalID, requestID, executionStatus, resultMessage string
	var errMessage, failureReason string
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT root_task_id, terminal_task_id, request_id, execution_status,
		       result_message, error, failure_reason
		FROM task_completion_outbox
		WHERE root_task_id = $1
	`, rootTaskID).Scan(
		&rootID,
		&terminalID,
		&requestID,
		&executionStatus,
		&resultMessage,
		&errMessage,
		&failureReason,
	); err != nil {
		t.Fatal(err)
	}
	if rootID != rootTaskID ||
		terminalID != terminalTaskID ||
		requestID != "multica-terminal:"+rootTaskID ||
		executionStatus != "failed" ||
		resultMessage != wantReply ||
		errMessage != "task cancelled" ||
		failureReason != "cancelled" {
		t.Fatalf(
			"cancelled completion = root:%s terminal:%s request:%s status:%s reply:%q error:%q reason:%q",
			rootID,
			terminalID,
			requestID,
			executionStatus,
			resultMessage,
			errMessage,
			failureReason,
		)
	}
}

func TestCancelledTaskOutboxIsCreatedAtomically(t *testing.T) {
	fixture := newCancelledCompletionFixture(t)
	taskID := fixture.taskIDs[0]
	prepareRemoteCancellationTask(t, fixture, taskID, "running")

	if _, err := fixture.queries.CancelAgentTask(
		context.Background(),
		util.MustParseUUID(taskID),
	); err != nil {
		t.Fatal(err)
	}
	assertCancelledCompletion(t, fixture, taskID, taskID, "")
}

func TestDeleteIssuePreservesCancelledTaskOutbox(t *testing.T) {
	fixture := newCancelledCompletionFixture(t)
	taskID := fixture.taskIDs[0]
	issueID := fixture.issueIDs[0]
	prepareRemoteCancellationTask(t, fixture, taskID, "running")

	if _, err := fixture.queries.CancelAgentTasksByIssue(
		context.Background(),
		util.MustParseUUID(issueID),
	); err != nil {
		t.Fatal(err)
	}
	if err := fixture.queries.DeleteIssue(
		context.Background(),
		db.DeleteIssueParams{
			ID:          util.MustParseUUID(issueID),
			WorkspaceID: util.MustParseUUID(fixture.workspaceID),
		},
	); err != nil {
		t.Fatal(err)
	}
	assertCancelledCompletion(t, fixture, taskID, taskID, "")
}

func TestBatchDeleteIssuesPreservesEveryCancelledTaskOutbox(t *testing.T) {
	fixture := newCancelledCompletionFixture(t)
	for index := range 2 {
		prepareRemoteCancellationTask(t, fixture, fixture.taskIDs[index], "running")
	}
	for index := range 2 {
		issueID := fixture.issueIDs[index]
		if _, err := fixture.queries.CancelAgentTasksByIssue(
			context.Background(),
			util.MustParseUUID(issueID),
		); err != nil {
			t.Fatal(err)
		}
		if err := fixture.queries.DeleteIssue(
			context.Background(),
			db.DeleteIssueParams{
				ID:          util.MustParseUUID(issueID),
				WorkspaceID: util.MustParseUUID(fixture.workspaceID),
			},
		); err != nil {
			t.Fatal(err)
		}
	}
	for index := range 2 {
		assertCancelledCompletion(
			t,
			fixture,
			fixture.taskIDs[index],
			fixture.taskIDs[index],
			"",
		)
	}
}

func TestDeleteChatSessionAndSystemAgentPreservesCancelledTaskOutbox(t *testing.T) {
	fixture := newCancelledCompletionFixture(t)
	taskID := fixture.taskIDs[0]
	if _, err := fixture.pool.Exec(context.Background(), `
		UPDATE agent
		SET kind = 'system', system_key = 'agent_builder:cancelled-completion-test'
		WHERE id = $1
	`, fixture.agentID); err != nil {
		t.Fatal(err)
	}
	session, err := fixture.queries.CreateChatSession(
		context.Background(),
		db.CreateChatSessionParams{
			WorkspaceID:  util.MustParseUUID(fixture.workspaceID),
			AgentID:      util.MustParseUUID(fixture.agentID),
			CreatorID:    util.MustParseUUID(fixture.userID),
			Title:        "cancelled completion test",
			IsAgentIntro: false,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(context.Background(), `
		UPDATE agent_task_queue
		SET issue_id = NULL, chat_session_id = $2
		WHERE id = $1
	`, taskID, session.ID); err != nil {
		t.Fatal(err)
	}
	prepareRemoteCancellationTask(t, fixture, taskID, "running")

	tx, err := fixture.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	qtx := fixture.queries.WithTx(tx)
	if _, err := qtx.CancelAgentTasksByChatSession(context.Background(), session.ID); err != nil {
		tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := qtx.DeleteChatSession(
		context.Background(),
		db.DeleteChatSessionParams{
			ID:          session.ID,
			WorkspaceID: util.MustParseUUID(fixture.workspaceID),
		},
	); err != nil {
		tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := qtx.DeleteSystemAgentByID(
		context.Background(),
		util.MustParseUUID(fixture.agentID),
	); err != nil {
		tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCancelledCompletion(t, fixture, taskID, taskID, "")
}

func TestArchiveAgentPreservesCancelledTaskOutbox(t *testing.T) {
	fixture := newCancelledCompletionFixture(t)
	taskID := fixture.taskIDs[0]
	prepareRemoteCancellationTask(t, fixture, taskID, "running")

	if _, err := fixture.queries.ArchiveAgent(
		context.Background(),
		db.ArchiveAgentParams{
			ID:         util.MustParseUUID(fixture.agentID),
			ArchivedBy: util.MustParseUUID(fixture.userID),
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.queries.CancelAgentTasksByAgent(
		context.Background(),
		util.MustParseUUID(fixture.agentID),
	); err != nil {
		t.Fatal(err)
	}
	assertCancelledCompletion(t, fixture, taskID, taskID, "")
}

func TestRuntimeCascadeCancelsDeferredTaskBeforeHardDelete(t *testing.T) {
	fixture := newCancelledCompletionFixture(t)
	taskID := fixture.taskIDs[0]
	prepareRemoteCancellationTask(t, fixture, taskID, "deferred")
	if _, err := fixture.pool.Exec(context.Background(), `
		UPDATE agent
		SET archived_at = now(), archived_by = $2
		WHERE id = $1
	`, fixture.agentID, fixture.userID); err != nil {
		t.Fatal(err)
	}

	tx, err := fixture.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	qtx := fixture.queries.WithTx(tx)
	if _, err := qtx.CancelAgentTasksByRuntimeOrAgent(
		context.Background(),
		db.CancelAgentTasksByRuntimeOrAgentParams{
			RuntimeIds: []pgtype.UUID{util.MustParseUUID(fixture.runtimeID)},
			AgentIds:   []pgtype.UUID{util.MustParseUUID(fixture.agentID)},
		},
	); err != nil {
		tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := qtx.DeleteArchivedAgentsByRuntime(
		context.Background(),
		util.MustParseUUID(fixture.runtimeID),
	); err != nil {
		tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := qtx.DeleteAgentRuntime(
		context.Background(),
		util.MustParseUUID(fixture.runtimeID),
	); err != nil {
		tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCancelledCompletion(t, fixture, taskID, taskID, "")
}

func TestCancelledTaskWithoutCallbackDoesNotCreateOutbox(t *testing.T) {
	fixture := newCancelledCompletionFixture(t)
	taskID := fixture.taskIDs[0]
	if _, err := fixture.pool.Exec(context.Background(), `
		UPDATE agent_task_queue
		SET status = 'running', started_at = now(), context = '{}'::jsonb
		WHERE id = $1
	`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.queries.CancelAgentTask(
		context.Background(),
		util.MustParseUUID(taskID),
	); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM task_completion_outbox WHERE root_task_id = $1
	`, taskID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("callback-absent cancellation created %d outbox rows", count)
	}
}

func TestCancelledTaskOutboxAllowsOnlyExactReplay(t *testing.T) {
	fixture := newCancelledCompletionFixture(t)
	taskID := fixture.taskIDs[0]
	prepareRemoteCancellationTask(t, fixture, taskID, "running")
	if _, err := fixture.queries.CancelAgentTask(
		context.Background(),
		util.MustParseUUID(taskID),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(context.Background(), `
		UPDATE agent_task_queue SET status = 'running' WHERE id = $1
	`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.queries.CancelAgentTask(
		context.Background(),
		util.MustParseUUID(taskID),
	); err != nil {
		t.Fatalf("exact cancellation replay: %v", err)
	}
	var count int
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM task_completion_outbox WHERE root_task_id = $1
	`, taskID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("exact cancellation replay created %d rows", count)
	}

	conflictTaskID := fixture.taskIDs[1]
	prepareRemoteCancellationTask(t, fixture, conflictTaskID, "running")
	if _, err := fixture.pool.Exec(context.Background(), `
		INSERT INTO task_completion_outbox (
			root_task_id, terminal_task_id, callback_url, target_identity,
			request_id, agent_id, execution_status, result_message,
			error, failure_reason
		)
		VALUES (
			$1::uuid, $1::uuid, $2, $3, 'multica-terminal:' || $1::text,
			$4, 'completed', 'different terminal result', NULL, NULL
		)
	`, conflictTaskID, cancelledCompletionCallback(conflictTaskID),
		taskCompletionTestTarget, fixture.agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.queries.CancelAgentTask(
		context.Background(),
		util.MustParseUUID(conflictTaskID),
	); err == nil {
		t.Fatal("conflicting cancellation completion was silently accepted")
	}
	var status string
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT status FROM agent_task_queue WHERE id = $1
	`, conflictTaskID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		t.Fatalf("conflicting cancellation status = %q, want running rollback", status)
	}
}

func TestCancelledRetryLeafUsesRootCallbackAndLatestReply(t *testing.T) {
	fixture := newCancelledCompletionFixture(t)
	rootTaskID := fixture.taskIDs[0]
	prepareRemoteCancellationTask(t, fixture, rootTaskID, "failed")
	if _, err := fixture.pool.Exec(context.Background(), `
		UPDATE agent_task_queue
		SET failure_reason = 'timeout', attempt = 1, max_attempts = 2
		WHERE id = $1
	`, rootTaskID); err != nil {
		t.Fatal(err)
	}
	child, err := fixture.queries.CreateRetryTask(
		context.Background(),
		db.CreateRetryTaskParams{
			ID:                   util.MustParseUUID(rootTaskID),
			RuntimeMcpOverlay:    []byte(`{}`),
			RuntimeConnectedApps: []byte(`[]`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	const latestReply = "latest partial reply"
	if _, err := fixture.pool.Exec(context.Background(), `
		INSERT INTO task_message (task_id, seq, type, content)
		VALUES ($1, 1, 'text', $2)
	`, child.ID, latestReply); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.queries.CancelAgentTask(
		context.Background(),
		child.ID,
	); err != nil {
		t.Fatal(err)
	}
	assertCancelledCompletion(
		t,
		fixture,
		rootTaskID,
		util.UUIDToString(child.ID),
		latestReply,
	)
}
