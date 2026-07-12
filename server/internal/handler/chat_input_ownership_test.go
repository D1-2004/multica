package handler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// setupDirectChatSession creates a runtime-guard agent (with its registered
// runtime + daemon) and a non-intro chat session for direct (web/mobile) chat
// ownership tests.
func setupDirectChatSession(t *testing.T, ctx context.Context, title string) (agentID, sessionID, runtimeID, daemonID string) {
	t.Helper()
	agentID, runtimeID, daemonID = createRuntimeGuardAgent(t, ctx)
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_session (workspace_id, agent_id, creator_id, title)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID, title).Scan(&sessionID); err != nil {
		t.Fatalf("setup: create chat session: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, sessionID) })
	return agentID, sessionID, runtimeID, daemonID
}

// sendDirectChat drives the transactional direct-send service path and returns
// the owning task id. The user message is created inside the same transaction
// with task_id = the new task, and the task owns its own input batch.
type directChatSendResult struct {
	TaskID    string
	Collected bool
}

func sendDirectChatResult(t *testing.T, ctx context.Context, agentID, sessionID, content string) directChatSendResult {
	t.Helper()
	sess, err := testHandler.Queries.GetChatSession(ctx, parseUUID(sessionID))
	if err != nil {
		t.Fatalf("load chat session: %v", err)
	}
	ag, err := testHandler.Queries.GetAgent(ctx, parseUUID(agentID))
	if err != nil {
		t.Fatalf("load agent: %v", err)
	}
	res, err := testHandler.TaskService.SendDirectChatMessage(ctx, sess, ag, parseUUID(testUserID), content, nil, "member", parseUUID(testUserID))
	if err != nil {
		t.Fatalf("SendDirectChatMessage: %v", err)
	}
	return directChatSendResult{
		TaskID:    uuidToString(res.Task.ID),
		Collected: res.Collected,
	}
}

func sendDirectChat(t *testing.T, ctx context.Context, agentID, sessionID, content string) string {
	t.Helper()
	return sendDirectChatResult(t, ctx, agentID, sessionID, content).TaskID
}

func markTaskRunning(t *testing.T, ctx context.Context, taskID string) {
	t.Helper()
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_task_queue SET status = 'running', started_at = now(), dispatched_at = now()
		WHERE id = $1
	`, taskID); err != nil {
		t.Fatalf("mark task running: %v", err)
	}
}

func completeResult(t *testing.T, output string) []byte {
	t.Helper()
	b, err := json.Marshal(TaskCompleteRequest{Output: output})
	if err != nil {
		t.Fatalf("marshal complete result: %v", err)
	}
	return b
}

func TestSendDirectChatMessageCollectsQueuedMessages(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID, sessionID, _, _ := setupDirectChatSession(t, ctx, "collector chat")

	first := sendDirectChatResult(t, ctx, agentID, sessionID, "first")
	second := sendDirectChatResult(t, ctx, agentID, sessionID, "second")

	if first.Collected {
		t.Fatal("first send must create a new task")
	}
	if !second.Collected {
		t.Fatal("second send must collect into the queued task")
	}
	if second.TaskID != first.TaskID {
		t.Fatalf("collector task mismatch: first=%s second=%s", first.TaskID, second.TaskID)
	}

	var taskCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue WHERE chat_session_id = $1
	`, sessionID).Scan(&taskCount); err != nil {
		t.Fatalf("count chat tasks: %v", err)
	}
	if taskCount != 1 {
		t.Fatalf("expected one queued collector, got %d tasks", taskCount)
	}

	owned, err := testHandler.Queries.ListChatInputMessages(ctx, parseUUID(first.TaskID))
	if err != nil {
		t.Fatalf("list collector input: %v", err)
	}
	if got := msgContents(owned); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("collector input = %#v, want [first second]", got)
	}
}

func TestSendDirectChatMessageCreatesCollectorBehindRunningTask(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID, sessionID, _, _ := setupDirectChatSession(t, ctx, "collector behind running chat")

	running := sendDirectChatResult(t, ctx, agentID, sessionID, "running")
	markTaskRunning(t, ctx, running.TaskID)
	next := sendDirectChatResult(t, ctx, agentID, sessionID, "next one")
	collected := sendDirectChatResult(t, ctx, agentID, sessionID, "next two")

	if next.Collected {
		t.Fatal("first send behind a running task must create the next collector")
	}
	if !collected.Collected || collected.TaskID != next.TaskID {
		t.Fatalf("later send must reuse next collector: next=%+v collected=%+v", next, collected)
	}
	if next.TaskID == running.TaskID {
		t.Fatal("running task input must remain sealed")
	}

	var taskCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue WHERE chat_session_id = $1
	`, sessionID).Scan(&taskCount); err != nil {
		t.Fatalf("count chat tasks: %v", err)
	}
	if taskCount != 2 {
		t.Fatalf("expected running task plus one collector, got %d tasks", taskCount)
	}

	owned, err := testHandler.Queries.ListChatInputMessages(ctx, parseUUID(next.TaskID))
	if err != nil {
		t.Fatalf("list next collector input: %v", err)
	}
	if got := msgContents(owned); len(got) != 2 || got[0] != "next one" || got[1] != "next two" {
		t.Fatalf("next collector input = %#v, want [next one next two]", got)
	}
}

func TestSendDirectChatMessageConcurrentCollector(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID, sessionID, _, _ := setupDirectChatSession(t, ctx, "concurrent collector chat")
	session, err := testHandler.Queries.GetChatSession(ctx, parseUUID(sessionID))
	if err != nil {
		t.Fatalf("load chat session: %v", err)
	}
	agent, err := testHandler.Queries.GetAgent(ctx, parseUUID(agentID))
	if err != nil {
		t.Fatalf("load agent: %v", err)
	}

	type result struct {
		taskID    string
		collected bool
		err       error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, content := range []string{"alpha", "beta"} {
		content := content
		go func() {
			<-start
			res, sendErr := testHandler.TaskService.SendDirectChatMessage(
				ctx, session, agent, parseUUID(testUserID), content, nil, "member", parseUUID(testUserID),
			)
			if sendErr != nil {
				results <- result{err: sendErr}
				return
			}
			results <- result{taskID: uuidToString(res.Task.ID), collected: res.Collected}
		}()
	}
	close(start)

	first := <-results
	second := <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent sends failed: first=%v second=%v", first.err, second.err)
	}
	if first.taskID != second.taskID {
		t.Fatalf("concurrent sends created different collectors: first=%s second=%s", first.taskID, second.taskID)
	}
	if first.collected == second.collected {
		t.Fatalf("expected exactly one collected result: first=%t second=%t", first.collected, second.collected)
	}

	var taskCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM agent_task_queue WHERE chat_session_id = $1
	`, sessionID).Scan(&taskCount); err != nil {
		t.Fatalf("count chat tasks: %v", err)
	}
	if taskCount != 1 {
		t.Fatalf("expected one queued collector after concurrent sends, got %d", taskCount)
	}
}

func TestQueuedDirectChatCollectorLocksTaskUntilMessageCommit(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID, sessionID, _, _ := setupDirectChatSession(t, ctx, "collector dispatch lock chat")
	taskID := sendDirectChat(t, ctx, agentID, sessionID, "first")

	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin collector transaction: %v", err)
	}
	defer tx.Rollback(ctx)
	qtx := db.New(tx)
	if _, err := qtx.LockChatSessionForDirectSend(ctx, parseUUID(sessionID)); err != nil {
		t.Fatalf("lock session: %v", err)
	}
	collector, err := qtx.GetQueuedDirectChatCollector(ctx, parseUUID(sessionID))
	if err != nil {
		t.Fatalf("select collector: %v", err)
	}
	if uuidToString(collector.ID) != taskID {
		t.Fatalf("collector = %s, want %s", uuidToString(collector.ID), taskID)
	}

	dispatchDone := make(chan error, 1)
	go func() {
		_, updateErr := testPool.Exec(ctx, `
			UPDATE agent_task_queue SET status = 'dispatched', dispatched_at = now()
			WHERE id = $1 AND status = 'queued'
		`, taskID)
		dispatchDone <- updateErr
	}()

	select {
	case updateErr := <-dispatchDone:
		t.Fatalf("dispatcher updated collector before message transaction committed: %v", updateErr)
	case <-time.After(100 * time.Millisecond):
		// Expected: SELECT ... FOR UPDATE keeps dispatch behind the send.
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit collector transaction: %v", err)
	}
	select {
	case updateErr := <-dispatchDone:
		if updateErr != nil {
			t.Fatalf("dispatcher update after commit: %v", updateErr)
		}
	case <-time.After(time.Second):
		t.Fatal("dispatcher remained blocked after collector transaction committed")
	}
}

// TestDirectChat_QueuedMessagesShareCollectorInputBatch pins the collector
// contract: messages arriving before dispatch share one task-owned batch and
// are delivered together in their original order.
func TestDirectChat_QueuedMessagesShareCollectorInputBatch(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID, sessionID, runtimeID, daemonID := setupDirectChatSession(t, ctx, "ownership chat")

	t1 := sendDirectChat(t, ctx, agentID, sessionID, "看上海天气")
	t2 := sendDirectChat(t, ctx, agentID, sessionID, "还有青岛")

	if t2 != t1 {
		t.Fatalf("queued messages must share one collector: first=%s second=%s", t1, t2)
	}
	assertTaskInputOwner(t, ctx, t1, t1)

	claimed := claimTaskForRuntimeGuard(t, runtimeID, daemonID)
	if claimed.ChatMessage != "看上海天气\n\n还有青岛" {
		t.Fatalf("collector claim must deliver both queued messages; got %q", claimed.ChatMessage)
	}
}

// TestDirectChat_RunningTaskDoesNotAbsorbNewMessage pins the acceptance case:
// while T1 is running, a message sent from another surface lands on T2 and is
// never folded into T1's already-sealed input batch.
func TestDirectChat_RunningTaskDoesNotAbsorbNewMessage(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID, sessionID, _, _ := setupDirectChatSession(t, ctx, "no-absorb chat")

	t1 := sendDirectChat(t, ctx, agentID, sessionID, "first")
	markTaskRunning(t, ctx, t1)
	// A second message arrives mid-run and gets its own task.
	t2 := sendDirectChat(t, ctx, agentID, sessionID, "second")
	if t1 == t2 {
		t.Fatal("second send must create a distinct task")
	}

	// T1's owned batch is still exactly {first}; the mid-run message belongs to T2.
	owned1, err := testHandler.Queries.ListChatInputMessages(ctx, parseUUID(t1))
	if err != nil {
		t.Fatalf("list T1 owned input: %v", err)
	}
	if len(owned1) != 1 || owned1[0].Content != "first" {
		t.Fatalf("T1 must own only its own message; got %+v", msgContents(owned1))
	}
	owned2, err := testHandler.Queries.ListChatInputMessages(ctx, parseUUID(t2))
	if err != nil {
		t.Fatalf("list T2 owned input: %v", err)
	}
	if len(owned2) != 1 || owned2[0].Content != "second" {
		t.Fatalf("T2 must own the mid-run message; got %+v", msgContents(owned2))
	}
}

// TestCompleteTask_ChatEmptyOutputWritesNoResponse: an empty final output is a
// visible, terminal no_response outcome — exactly one assistant row with
// message_kind='no_response' and a non-empty fallback body, task completed, and
// no auto-retry.
func TestCompleteTask_ChatEmptyOutputWritesNoResponse(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID, sessionID, _, _ := setupDirectChatSession(t, ctx, "no-response chat")
	taskID := sendDirectChat(t, ctx, agentID, sessionID, "do a tool-only thing")
	markTaskRunning(t, ctx, taskID)

	// Whitespace-only output trims to empty → no_response.
	if _, err := testHandler.TaskService.CompleteTask(ctx, parseUUID(taskID), completeResult(t, "   "), "", ""); err != nil {
		t.Fatalf("complete task: %v", err)
	}

	rows := assistantRows(t, ctx, sessionID)
	if len(rows) != 1 {
		t.Fatalf("expected exactly one assistant outcome, got %d", len(rows))
	}
	if rows[0].MessageKind != protocol.ChatMessageKindNoResponse {
		t.Fatalf("expected message_kind=no_response, got %q", rows[0].MessageKind)
	}
	if rows[0].Content == "" {
		t.Fatal("no_response row must carry a non-empty fallback body for old clients")
	}
	assertTaskStatus(t, ctx, taskID, "completed")
	assertNoRetryChild(t, ctx, taskID)
}

// TestCompleteTask_ChatNonEmptyOutputWritesMessage: a normal reply becomes a
// single ordinary assistant message.
func TestCompleteTask_ChatNonEmptyOutputWritesMessage(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID, sessionID, _, _ := setupDirectChatSession(t, ctx, "reply chat")
	taskID := sendDirectChat(t, ctx, agentID, sessionID, "hello")
	markTaskRunning(t, ctx, taskID)

	if _, err := testHandler.TaskService.CompleteTask(ctx, parseUUID(taskID), completeResult(t, "hi there"), "sess-1", "/tmp/wd"); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	rows := assistantRows(t, ctx, sessionID)
	if len(rows) != 1 {
		t.Fatalf("expected exactly one assistant message, got %d", len(rows))
	}
	if rows[0].MessageKind != protocol.ChatMessageKindMessage {
		t.Fatalf("expected message_kind=message, got %q", rows[0].MessageKind)
	}
	if rows[0].Content != "hi there" {
		t.Fatalf("expected content 'hi there', got %q", rows[0].Content)
	}
}

// TestCompleteTask_ChatCallbackIdempotent: a replayed completion callback must
// not write a second assistant outcome.
func TestCompleteTask_ChatCallbackIdempotent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID, sessionID, _, _ := setupDirectChatSession(t, ctx, "idempotent chat")
	taskID := sendDirectChat(t, ctx, agentID, sessionID, "hey")
	markTaskRunning(t, ctx, taskID)

	res := completeResult(t, "reply")
	if _, err := testHandler.TaskService.CompleteTask(ctx, parseUUID(taskID), res, "", ""); err != nil {
		t.Fatalf("first complete: %v", err)
	}
	// Replay: the status CAS fails, so this is an idempotent no-op success.
	if _, err := testHandler.TaskService.CompleteTask(ctx, parseUUID(taskID), res, "", ""); err != nil {
		t.Fatalf("replayed complete must be idempotent success, got %v", err)
	}
	if rows := assistantRows(t, ctx, sessionID); len(rows) != 1 {
		t.Fatalf("expected exactly one assistant outcome after replay, got %d", len(rows))
	}
}

// TestFailTask_ChatRetryInheritsInputOwnerAndPriority: a transient failure of a
// task-owned direct task creates a retry child that reuses the SAME input owner
// (so it reads the same user messages) and is queued at a bumped priority so it
// is claimed ahead of fresh chat tasks.
func TestFailTask_ChatRetryInheritsInputOwnerAndPriority(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID, sessionID, _, _ := setupDirectChatSession(t, ctx, "retry chat")
	rootID := sendDirectChat(t, ctx, agentID, sessionID, "root question")
	markTaskRunning(t, ctx, rootID)

	if _, err := testHandler.TaskService.FailTask(ctx, parseUUID(rootID), "runtime went away", "", "", "runtime_offline"); err != nil {
		t.Fatalf("fail task: %v", err)
	}

	var childID, childOwner string
	var childPriority, childAttempt int
	var childStatus string
	if err := testPool.QueryRow(ctx, `
		SELECT id, chat_input_task_id, priority, status, attempt
		FROM agent_task_queue
		WHERE parent_task_id = $1
	`, rootID).Scan(&childID, &childOwner, &childPriority, &childStatus, &childAttempt); err != nil {
		t.Fatalf("expected a retry child, got: %v", err)
	}
	if childOwner != rootID {
		t.Fatalf("retry child must inherit the root input owner %s, got %s", rootID, childOwner)
	}
	if childPriority < 3 {
		t.Fatalf("chat retry must be bumped above fresh chat priority (2); got %d", childPriority)
	}
	if childStatus != "queued" {
		t.Fatalf("retry child must be queued, got %q", childStatus)
	}
	// The root direct task starts at attempt 1, so its first retry is attempt 2.
	var rootAttempt int
	if err := testPool.QueryRow(ctx, `SELECT attempt FROM agent_task_queue WHERE id = $1`, rootID).Scan(&rootAttempt); err != nil {
		t.Fatalf("read root attempt: %v", err)
	}
	if childAttempt != rootAttempt+1 {
		t.Fatalf("retry child attempt must be root+1 (%d), got %d", rootAttempt+1, childAttempt)
	}

	// The child reads the same input batch: the root's user message.
	owned, err := testHandler.Queries.ListChatInputMessages(ctx, parseUUID(childOwner))
	if err != nil {
		t.Fatalf("list child owned input: %v", err)
	}
	if len(owned) != 1 || owned[0].Content != "root question" {
		t.Fatalf("retry child must read the root input batch; got %+v", msgContents(owned))
	}

	// A retry child replays the root-owned input batch, so it must never become
	// the collector for a newly-arriving message. That message needs a fresh
	// self-owned task whose input will actually be loaded when claimed.
	next := sendDirectChatResult(t, ctx, agentID, sessionID, "new question")
	if next.Collected {
		t.Fatal("new message must not collect into a queued retry child")
	}
	if next.TaskID == childID {
		t.Fatal("new message must create a fresh collector behind the retry child")
	}
	assertTaskInputOwner(t, ctx, next.TaskID, next.TaskID)
	nextOwned, err := testHandler.Queries.ListChatInputMessages(ctx, parseUUID(next.TaskID))
	if err != nil {
		t.Fatalf("list fresh collector input: %v", err)
	}
	if len(nextOwned) != 1 || nextOwned[0].Content != "new question" {
		t.Fatalf("fresh collector must own the new message; got %+v", msgContents(nextOwned))
	}
}

// ---- helpers ----

func msgContents(msgs []db.ChatMessage) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Content)
	}
	return out
}

func assistantRows(t *testing.T, ctx context.Context, sessionID string) []db.ChatMessage {
	t.Helper()
	all, err := testHandler.Queries.ListChatMessages(ctx, parseUUID(sessionID))
	if err != nil {
		t.Fatalf("list chat messages: %v", err)
	}
	var out []db.ChatMessage
	for _, m := range all {
		if m.Role == "assistant" {
			out = append(out, m)
		}
	}
	return out
}

func assertTaskInputOwner(t *testing.T, ctx context.Context, taskID, wantOwner string) {
	t.Helper()
	var owner string
	if err := testPool.QueryRow(ctx, `SELECT chat_input_task_id FROM agent_task_queue WHERE id = $1`, taskID).Scan(&owner); err != nil {
		t.Fatalf("read chat_input_task_id: %v", err)
	}
	if owner != wantOwner {
		t.Fatalf("task %s input owner = %s, want %s", taskID, owner, wantOwner)
	}
}

func assertTaskStatus(t *testing.T, ctx context.Context, taskID, want string) {
	t.Helper()
	var status string
	if err := testPool.QueryRow(ctx, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	if status != want {
		t.Fatalf("task %s status = %q, want %q", taskID, status, want)
	}
}

func assertNoRetryChild(t *testing.T, ctx context.Context, taskID string) {
	t.Helper()
	var n int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE parent_task_id = $1`, taskID).Scan(&n); err != nil {
		t.Fatalf("count retry children: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected no retry child for a completed no_response turn, got %d", n)
	}
}

// insertChannelChatTask creates a running chat task with chat_input_task_id NULL
// — the legacy/channel (Slack/Lark) shape — directly, bypassing the task-owned
// direct-send path.
func insertChannelChatTask(t *testing.T, ctx context.Context, agentID, runtimeID, sessionID string) string {
	t.Helper()
	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, chat_session_id, status, priority, started_at, dispatched_at)
		VALUES ($1, $2, $3, 'running', 2, now(), now())
		RETURNING id
	`, agentID, runtimeID, sessionID).Scan(&taskID); err != nil {
		t.Fatalf("setup: create channel chat task: %v", err)
	}
	return taskID
}

// TestCompleteTask_ChannelEmptyOutputWritesNoRow pins the MUL-4351 review fix:
// a legacy/channel task (chat_input_task_id NULL) that completes with empty
// output must NOT write an assistant row — so chat:done carries empty content
// and the Slack/Lark outbound keeps silently dropping it. The no_response
// fallback body must never reach an external channel. A non-empty channel
// completion still writes an ordinary message.
func TestCompleteTask_ChannelEmptyOutputWritesNoRow(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID, sessionID, runtimeID, _ := setupDirectChatSession(t, ctx, "channel-like chat")

	// Empty output → no row at all.
	emptyTask := insertChannelChatTask(t, ctx, agentID, runtimeID, sessionID)
	if _, err := testHandler.TaskService.CompleteTask(ctx, parseUUID(emptyTask), completeResult(t, "   "), "", ""); err != nil {
		t.Fatalf("complete channel task (empty): %v", err)
	}
	if rows := assistantRows(t, ctx, sessionID); len(rows) != 0 {
		t.Fatalf("channel empty completion must write NO assistant row (Slack/Lark silent-drop), got %d", len(rows))
	}

	// Non-empty output → one ordinary message (kind 'message', not no_response).
	textTask := insertChannelChatTask(t, ctx, agentID, runtimeID, sessionID)
	if _, err := testHandler.TaskService.CompleteTask(ctx, parseUUID(textTask), completeResult(t, "channel reply"), "", ""); err != nil {
		t.Fatalf("complete channel task (text): %v", err)
	}
	rows := assistantRows(t, ctx, sessionID)
	if len(rows) != 1 {
		t.Fatalf("channel non-empty completion must write exactly one message, got %d", len(rows))
	}
	if rows[0].MessageKind != protocol.ChatMessageKindMessage || rows[0].Content != "channel reply" {
		t.Fatalf("channel message = kind %q content %q, want message/'channel reply'", rows[0].MessageKind, rows[0].Content)
	}
}
