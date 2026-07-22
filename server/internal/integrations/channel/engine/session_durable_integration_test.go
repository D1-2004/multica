package engine

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type durableSessionFixture struct {
	pool           *pgxpool.Pool
	userID         pgtype.UUID
	workspaceID    pgtype.UUID
	runtimeID      pgtype.UUID
	agentID        pgtype.UUID
	sessionID      pgtype.UUID
	installationID pgtype.UUID
	chatID         string
}

func newDurableSessionFixture(t *testing.T) durableSessionFixture {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database unreachable: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `
		CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_task_queue_deferred_chat_session
		ON agent_task_queue (chat_session_id)
		WHERE status = 'deferred' AND chat_session_id IS NOT NULL
	`); err != nil {
		t.Fatalf("ensure deferred channel-task index: %v", err)
	}

	suffix := time.Now().UnixNano()
	f := durableSessionFixture{pool: pool, chatID: fmt.Sprintf("durable-chat-%d", suffix)}
	if err := pool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ('Durable Session User', $1)
		RETURNING id
	`, fmt.Sprintf("durable-session-%d@multica.test", suffix)).Scan(&f.userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ('Durable Session Workspace', $1, '', 'DSI')
		RETURNING id
	`, fmt.Sprintf("durable-session-%d", suffix)).Scan(&f.workspaceID); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, f.workspaceID, f.userID); err != nil {
		t.Fatalf("create member: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, last_seen_at, visibility, owner_id
		)
		VALUES ($1, $2, 'Durable Session Runtime', 'cloud', 'durable_session_test',
		        'online', 'test', '{}'::jsonb, now(), 'private', $3)
		RETURNING id
	`, f.workspaceID, fmt.Sprintf("durable-session-daemon-%d", suffix), f.userID).Scan(&f.runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id
		)
		VALUES ($1, 'Durable Session Agent', '', 'cloud', '{}'::jsonb,
		        $2, 'private', 5, $3)
		RETURNING id
	`, f.workspaceID, f.runtimeID, f.userID).Scan(&f.agentID); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO chat_session (workspace_id, agent_id, creator_id, title)
		VALUES ($1, $2, $3, 'Durable session transaction test')
		RETURNING id
	`, f.workspaceID, f.agentID, f.userID).Scan(&f.sessionID); err != nil {
		t.Fatalf("create chat session: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO channel_installation (
			workspace_id, agent_id, channel_type, config, status, installer_user_id
		)
		VALUES ($1, $2, 'feishu', jsonb_build_object('app_id', $3), 'active', $4)
		RETURNING id
	`, f.workspaceID, f.agentID, fmt.Sprintf("durable-session-app-%d", suffix), f.userID).Scan(&f.installationID); err != nil {
		t.Fatalf("create channel installation: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO channel_chat_session_binding (
			chat_session_id, installation_id, channel_type, channel_chat_id, chat_type, config
		)
		VALUES ($1, $2, 'feishu', $3, 'p2p', '{}'::jsonb)
	`, f.sessionID, f.installationID, f.chatID); err != nil {
		t.Fatalf("create channel session binding: %v", err)
	}

	t.Cleanup(func() {
		cleanup := context.Background()
		pool.Exec(cleanup, `DELETE FROM channel_inbound_message_dedup WHERE installation_id = $1`, f.installationID)
		pool.Exec(cleanup, `DELETE FROM channel_chat_session_binding WHERE installation_id = $1`, f.installationID)
		pool.Exec(cleanup, `DELETE FROM channel_installation WHERE id = $1`, f.installationID)
		pool.Exec(cleanup, `DELETE FROM agent_task_queue WHERE agent_id = $1`, f.agentID)
		pool.Exec(cleanup, `DELETE FROM chat_message WHERE chat_session_id = $1`, f.sessionID)
		pool.Exec(cleanup, `DELETE FROM chat_session WHERE id = $1`, f.sessionID)
		pool.Exec(cleanup, `DELETE FROM agent WHERE id = $1`, f.agentID)
		pool.Exec(cleanup, `DELETE FROM agent_runtime WHERE id = $1`, f.runtimeID)
		pool.Exec(cleanup, `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, f.workspaceID, f.userID)
		pool.Exec(cleanup, `DELETE FROM workspace WHERE id = $1`, f.workspaceID)
		pool.Exec(cleanup, `DELETE FROM "user" WHERE id = $1`, f.userID)
	})
	return f
}

func newPreparedDurableSessionTask(f durableSessionFixture, body string) *service.PreparedChannelChatTask {
	id := uuid.New()
	return &service.PreparedChannelChatTask{
		ID:               pgtype.UUID{Bytes: [16]byte(id), Valid: true},
		AgentID:          f.agentID,
		RuntimeID:        f.runtimeID,
		InitiatorUserID:  f.userID,
		OriginatorUserID: f.userID,
		TaskContext:      []byte(fmt.Sprintf(`{"body":%q}`, body)),
		DebounceSeconds:  service.ChannelChatDebounceWindow.Seconds(),
	}
}

func claimDurableSessionMessage(t *testing.T, f durableSessionFixture, messageID string) db.ChannelInboundMessageDedup {
	t.Helper()
	row, err := db.New(f.pool).ClaimChannelInboundDedup(context.Background(), db.ClaimChannelInboundDedupParams{
		InstallationID: f.installationID,
		MessageID:      messageID,
	})
	if err != nil {
		t.Fatalf("claim dedup %s: %v", messageID, err)
	}
	return row
}

func TestAppendUserMessageDurableTransactionRollbackAndBatchSeal(t *testing.T) {
	f := newDurableSessionFixture(t)
	ctx := context.Background()
	q := db.New(f.pool)
	session := NewChatSession(q, f.pool, channel.TypeFeishu, SessionTitles{Direct: "direct"})

	badClaim := claimDurableSessionMessage(t, f, "durable-rollback")
	wrongToken := badClaim.ClaimToken
	wrongToken.Bytes[0] ^= 0xff
	if _, err := session.AppendUserMessage(ctx, AppendInput{
		SessionID: f.sessionID, WorkspaceID: f.workspaceID, Sender: f.userID,
		InstallationID: f.installationID, Body: "must roll back", MessageID: "durable-rollback",
		ClaimToken: wrongToken, PreparedTask: newPreparedDurableSessionTask(f, "must roll back"),
	}); err != ErrClaimLost {
		t.Fatalf("wrong claim error = %v, want ErrClaimLost", err)
	}
	var taskCount, messageCount int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE chat_session_id = $1`, f.sessionID).Scan(&taskCount); err != nil {
		t.Fatalf("count rolled-back tasks: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM chat_message WHERE chat_session_id = $1`, f.sessionID).Scan(&messageCount); err != nil {
		t.Fatalf("count rolled-back messages: %v", err)
	}
	binding, err := q.GetChannelChatSessionBinding(ctx, db.GetChannelChatSessionBindingParams{
		InstallationID: f.installationID, ChannelChatID: f.chatID,
	})
	if err != nil {
		t.Fatalf("load binding after rollback: %v", err)
	}
	if taskCount != 0 || messageCount != 0 || binding.LastMessageID.Valid {
		t.Fatalf("partial durable append survived rollback: tasks=%d messages=%d reply_target=%v", taskCount, messageCount, binding.LastMessageID)
	}

	appendOne := func(messageID, body string) AppendResult {
		claim := claimDurableSessionMessage(t, f, messageID)
		res, err := session.AppendUserMessage(ctx, AppendInput{
			SessionID: f.sessionID, WorkspaceID: f.workspaceID, Sender: f.userID,
			InstallationID: f.installationID, Body: body, MessageID: messageID,
			ClaimToken: claim.ClaimToken, PreparedTask: newPreparedDurableSessionTask(f, body),
		})
		if err != nil {
			t.Fatalf("append %s: %v", messageID, err)
		}
		return res
	}

	first := appendOne("durable-one", "one")
	second := appendOne("durable-two", "two")
	if first.TaskID != second.TaskID {
		t.Fatalf("messages inside debounce window got different tasks: %v vs %v", first.TaskID, second.TaskID)
	}
	var distinctTaskIDs, batchedMessages int
	if err := f.pool.QueryRow(ctx, `
		SELECT count(DISTINCT task_id), count(*)
		FROM chat_message
		WHERE chat_session_id = $1 AND content IN ('one', 'two')
	`, f.sessionID).Scan(&distinctTaskIDs, &batchedMessages); err != nil {
		t.Fatalf("inspect durable input batch: %v", err)
	}
	if distinctTaskIDs != 1 || batchedMessages != 2 {
		t.Fatalf("durable batch = distinct tasks %d, messages %d; want 1/2", distinctTaskIDs, batchedMessages)
	}

	if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'queued' WHERE id = $1`, first.TaskID); err != nil {
		t.Fatalf("seal first durable batch: %v", err)
	}
	third := appendOne("durable-three", "three")
	if third.TaskID == first.TaskID {
		t.Fatal("message after promoter seal reused the queued task")
	}
	var thirdTaskID pgtype.UUID
	if err := f.pool.QueryRow(ctx, `SELECT task_id FROM chat_message WHERE chat_session_id = $1 AND content = 'three'`, f.sessionID).Scan(&thirdTaskID); err != nil {
		t.Fatalf("load post-seal message task: %v", err)
	}
	if thirdTaskID != third.TaskID {
		t.Fatalf("post-seal message task = %v, want %v", thirdTaskID, third.TaskID)
	}
}

func pendingFreshCount(t *testing.T, f durableSessionFixture) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM chat_session_pending_fresh WHERE chat_session_id = $1
	`, f.sessionID).Scan(&count); err != nil {
		t.Fatalf("count pending fresh session: %v", err)
	}
	return count
}

func TestPendingFreshSessionCrossReplicaRollbackAndSingleConsumption(t *testing.T) {
	f := newDurableSessionFixture(t)
	ctx := context.Background()
	q := db.New(f.pool)
	replicaA := NewChatSession(q, f.pool, channel.Type("dingtalk"), SessionTitles{Direct: "direct"})
	replicaB := NewChatSession(q, f.pool, channel.Type("dingtalk"), SessionTitles{Direct: "direct"})

	resetClaim := claimDurableSessionMessage(t, f, "pending-reset")
	marked, err := replicaA.PersistPendingFreshSession(ctx, PendingFreshSessionParams{
		SessionID:      f.sessionID,
		InstallationID: f.installationID,
		MessageID:      "pending-reset",
		ClaimToken:     resetClaim.ClaimToken,
	})
	if err != nil || !marked {
		t.Fatalf("persist pending reset = marked %t, err %v", marked, err)
	}
	if count := pendingFreshCount(t, f); count != 1 {
		t.Fatalf("pending rows = %d, want 1", count)
	}

	badClaim := claimDurableSessionMessage(t, f, "pending-rollback")
	wrongToken := badClaim.ClaimToken
	wrongToken.Bytes[0] ^= 0xff
	if _, err := replicaB.AppendUserMessage(ctx, AppendInput{
		SessionID: f.sessionID, WorkspaceID: f.workspaceID, Sender: f.userID,
		InstallationID: f.installationID, Body: "must roll back pending", MessageID: "pending-rollback",
		ClaimToken: wrongToken, PreparedTask: newPreparedDurableSessionTask(f, "must roll back pending"),
	}); err != ErrClaimLost {
		t.Fatalf("wrong claim error = %v, want ErrClaimLost", err)
	}
	if count := pendingFreshCount(t, f); count != 1 {
		t.Fatalf("rollback cleared pending rows: got %d, want 1", count)
	}

	goodClaim := claimDurableSessionMessage(t, f, "pending-consume")
	first, err := replicaB.AppendUserMessage(ctx, AppendInput{
		SessionID: f.sessionID, WorkspaceID: f.workspaceID, Sender: f.userID,
		InstallationID: f.installationID, Body: "consume pending", MessageID: "pending-consume",
		ClaimToken: goodClaim.ClaimToken, PreparedTask: newPreparedDurableSessionTask(f, "consume pending"),
	})
	if err != nil {
		t.Fatalf("consume pending: %v", err)
	}
	firstTask, err := q.GetAgentTask(ctx, first.TaskID)
	if err != nil {
		t.Fatalf("load first task: %v", err)
	}
	if !firstTask.ForceFreshSession || pendingFreshCount(t, f) != 0 {
		t.Fatalf("first task fresh/pending = %t/%d, want true/0", firstTask.ForceFreshSession, pendingFreshCount(t, f))
	}

	if _, err := f.pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'queued' WHERE id = $1`, first.TaskID); err != nil {
		t.Fatalf("seal first task: %v", err)
	}
	thirdClaim := claimDurableSessionMessage(t, f, "pending-third")
	third, err := replicaA.AppendUserMessage(ctx, AppendInput{
		SessionID: f.sessionID, WorkspaceID: f.workspaceID, Sender: f.userID,
		InstallationID: f.installationID, Body: "third", MessageID: "pending-third",
		ClaimToken: thirdClaim.ClaimToken, PreparedTask: newPreparedDurableSessionTask(f, "third"),
	})
	if err != nil {
		t.Fatalf("append third: %v", err)
	}
	thirdTask, err := q.GetAgentTask(ctx, third.TaskID)
	if err != nil {
		t.Fatalf("load third task: %v", err)
	}
	if thirdTask.ForceFreshSession {
		t.Fatal("pending fresh request was consumed more than once")
	}
}

func TestPendingFreshSessionConcurrentMessagesKeepFreshOnSharedBatch(t *testing.T) {
	f := newDurableSessionFixture(t)
	ctx := context.Background()
	q := db.New(f.pool)
	replicaA := NewChatSession(q, f.pool, channel.Type("dingtalk"), SessionTitles{Direct: "direct"})
	replicaB := NewChatSession(q, f.pool, channel.Type("dingtalk"), SessionTitles{Direct: "direct"})

	resetClaim := claimDurableSessionMessage(t, f, "concurrent-reset")
	if _, err := replicaA.PersistPendingFreshSession(ctx, PendingFreshSessionParams{
		SessionID: f.sessionID, InstallationID: f.installationID,
		MessageID: "concurrent-reset", ClaimToken: resetClaim.ClaimToken,
	}); err != nil {
		t.Fatalf("persist reset: %v", err)
	}

	type pendingAppend struct {
		session   *ChatSession
		messageID string
		body      string
		claim     db.ChannelInboundMessageDedup
	}
	appends := []pendingAppend{
		{session: replicaA, messageID: "concurrent-one", body: "one", claim: claimDurableSessionMessage(t, f, "concurrent-one")},
		{session: replicaB, messageID: "concurrent-two", body: "two", claim: claimDurableSessionMessage(t, f, "concurrent-two")},
	}
	start := make(chan struct{})
	results := make(chan AppendResult, len(appends))
	errs := make(chan error, len(appends))
	var wg sync.WaitGroup
	for _, appendCall := range appends {
		appendCall := appendCall
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := appendCall.session.AppendUserMessage(ctx, AppendInput{
				SessionID: f.sessionID, WorkspaceID: f.workspaceID, Sender: f.userID,
				InstallationID: f.installationID, Body: appendCall.body, MessageID: appendCall.messageID,
				ClaimToken: appendCall.claim.ClaimToken, PreparedTask: newPreparedDurableSessionTask(f, appendCall.body),
			})
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent append: %v", err)
		}
	}
	var taskID pgtype.UUID
	for result := range results {
		if !taskID.Valid {
			taskID = result.TaskID
		} else if result.TaskID != taskID {
			t.Fatalf("concurrent messages used different deferred tasks: %v vs %v", taskID, result.TaskID)
		}
	}
	task, err := q.GetAgentTask(ctx, taskID)
	if err != nil {
		t.Fatalf("load shared task: %v", err)
	}
	if !task.ForceFreshSession || pendingFreshCount(t, f) != 0 {
		t.Fatalf("shared task fresh/pending = %t/%d, want true/0", task.ForceFreshSession, pendingFreshCount(t, f))
	}
}

func TestPendingFreshSessionUncommittedResetSerializesNextRunnableMessage(t *testing.T) {
	f := newDurableSessionFixture(t)
	ctx := context.Background()
	q := db.New(f.pool)
	replica := NewChatSession(q, f.pool, channel.Type("dingtalk"), SessionTitles{Direct: "direct"})

	resetTx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin reset transaction: %v", err)
	}
	defer resetTx.Rollback(ctx)
	if _, err := resetTx.Exec(ctx, `SELECT id FROM chat_session WHERE id = $1 FOR UPDATE`, f.sessionID); err != nil {
		t.Fatalf("lock reset session: %v", err)
	}
	if _, err := resetTx.Exec(ctx, `
		INSERT INTO chat_session_pending_fresh (chat_session_id)
		VALUES ($1)
		ON CONFLICT (chat_session_id) DO NOTHING
	`, f.sessionID); err != nil {
		t.Fatalf("insert uncommitted reset: %v", err)
	}

	claim := claimDurableSessionMessage(t, f, "uncommitted-reset-next")
	type appendOutcome struct {
		result AppendResult
		err    error
	}
	started := make(chan struct{})
	done := make(chan appendOutcome, 1)
	go func() {
		close(started)
		result, err := replica.AppendUserMessage(ctx, AppendInput{
			SessionID: f.sessionID, WorkspaceID: f.workspaceID, Sender: f.userID,
			InstallationID: f.installationID, Body: "next after reset", MessageID: "uncommitted-reset-next",
			ClaimToken: claim.ClaimToken, PreparedTask: newPreparedDurableSessionTask(f, "next after reset"),
		})
		done <- appendOutcome{result: result, err: err}
	}()
	<-started
	select {
	case outcome := <-done:
		t.Fatalf("next runnable message bypassed the uncommitted reset lock: result=%+v err=%v", outcome.result, outcome.err)
	case <-time.After(150 * time.Millisecond):
	}

	if err := resetTx.Commit(ctx); err != nil {
		t.Fatalf("commit reset transaction: %v", err)
	}
	select {
	case outcome := <-done:
		if outcome.err != nil {
			t.Fatalf("append after reset commit: %v", outcome.err)
		}
		task, err := q.GetAgentTask(ctx, outcome.result.TaskID)
		if err != nil {
			t.Fatalf("load task after reset commit: %v", err)
		}
		if !task.ForceFreshSession || pendingFreshCount(t, f) != 0 {
			t.Fatalf("task fresh/pending = %t/%d, want true/0", task.ForceFreshSession, pendingFreshCount(t, f))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("next runnable message stayed blocked after reset commit")
	}
}
