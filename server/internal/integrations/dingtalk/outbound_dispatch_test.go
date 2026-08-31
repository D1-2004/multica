package dingtalk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type dispatchLifecycleQueries struct {
	inst    db.ChannelInstallation
	binding db.ChannelChatSessionBinding
	task    db.AgentTaskQueue

	mu     sync.Mutex
	claims map[string]bool

	stream    *fakeStreamEmotionQueries
	lastReply string
}

func (q *dispatchLifecycleQueries) GetChannelChatSessionBindingBySession(context.Context, db.GetChannelChatSessionBindingBySessionParams) (db.ChannelChatSessionBinding, error) {
	if q.binding.ChatSessionID.Valid || len(q.binding.Config) > 0 || q.binding.ChannelChatID != "" {
		return q.binding, nil
	}
	return db.ChannelChatSessionBinding{}, pgx.ErrNoRows
}

func (q *dispatchLifecycleQueries) GetChannelInstallation(context.Context, db.GetChannelInstallationParams) (db.ChannelInstallation, error) {
	if q.inst.ID.Valid {
		return q.inst, nil
	}
	return db.ChannelInstallation{}, pgx.ErrNoRows
}

func (q *dispatchLifecycleQueries) GetDingTalkProcessingEmotionByTask(ctx context.Context, taskID pgtype.UUID) (db.DingtalkProcessingEmotion, error) {
	if q.stream == nil {
		return db.DingtalkProcessingEmotion{}, pgx.ErrNoRows
	}
	return q.stream.GetDingTalkProcessingEmotionByTask(ctx, taskID)
}

func (q *dispatchLifecycleQueries) ListPendingChatMessagePreviewsAfterTask(context.Context, pgtype.UUID) ([]db.ListPendingChatMessagePreviewsAfterTaskRow, error) {
	return nil, nil
}

func (q *dispatchLifecycleQueries) GetDingTalkAccountBindingByAgent(context.Context, db.GetDingTalkAccountBindingByAgentParams) (db.ChannelInstallation, error) {
	return q.inst, nil
}

func (q *dispatchLifecycleQueries) GetAgentTask(context.Context, pgtype.UUID) (db.AgentTaskQueue, error) {
	return q.task, nil
}

func (q *dispatchLifecycleQueries) ClaimDispatchOutbound(context.Context, pgtype.UUID) (bool, error) {
	return q.claim("outbound"), nil
}

func (q *dispatchLifecycleQueries) ClaimDispatchProcessingReaction(context.Context, pgtype.UUID) (bool, error) {
	return q.claim("processing"), nil
}

func (q *dispatchLifecycleQueries) ClaimDispatchProcessingRecall(context.Context, pgtype.UUID) (bool, error) {
	return q.claim("recall"), nil
}

func (q *dispatchLifecycleQueries) claim(name string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.claims == nil {
		q.claims = make(map[string]bool)
	}
	if q.claims[name] {
		return false
	}
	q.claims[name] = true
	return true
}

func (q *dispatchLifecycleQueries) GetDingTalkProcessingEmotionBySourceMessage(ctx context.Context, sourceMessageID string) (db.DingtalkProcessingEmotion, error) {
	if q.stream == nil {
		return db.DingtalkProcessingEmotion{}, pgx.ErrNoRows
	}
	return q.stream.GetDingTalkProcessingEmotionBySourceMessage(ctx, sourceMessageID)
}

func (q *dispatchLifecycleQueries) GetLastTaskReplyText(context.Context, pgtype.UUID) (pgtype.Text, error) {
	if strings.TrimSpace(q.lastReply) == "" {
		return pgtype.Text{}, pgx.ErrNoRows
	}
	return pgtype.Text{String: q.lastReply, Valid: true}, nil
}

type dispatchRobotRecorder struct {
	mu       sync.Mutex
	sequence []string
	bodies   map[string][]map[string]any
}

func newDispatchRobotServer(t *testing.T) (*dispatchRobotRecorder, *httptest.Server) {
	t.Helper()
	recorder := &dispatchRobotRecorder{bodies: make(map[string][]map[string]any)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1.0/oauth2/accessToken" {
			_, _ = w.Write([]byte(`{"accessToken":"tok_test","expireIn":7200}`))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode %s body: %v", r.URL.Path, err)
		}
		recorder.mu.Lock()
		recorder.sequence = append(recorder.sequence, r.URL.Path)
		recorder.bodies[r.URL.Path] = append(recorder.bodies[r.URL.Path], body)
		recorder.mu.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return recorder, server
}

func dispatchLifecyclePayload(taskID, workspaceID, agentID, conversationType string) map[string]any {
	return map[string]any{
		"task_id":                  taskID,
		"issue_id":                 "11111111-1111-1111-1111-111111111111",
		"workspace_id":             workspaceID,
		"agent_id":                 agentID,
		"dispatch_idempotency_key": "dispatch-window:window-1",
		"dispatch_source":          map[string]any{"platform": "dingtalk", "type": "robot"},
		"dispatch_outbound":        map[string]any{"mode": "robot_sdk", "replyTo": "latest_message"},
		"dispatch_event_data": map[string]any{
			"conversation": map[string]any{"openConversationId": "cid-1", "type": conversationType},
			"sender":       map[string]any{"staffId": "staff-1"},
			"messages": []any{
				map[string]any{"openMsgId": "msg-1", "occurredAt": float64(10), "text": "first"},
				map[string]any{"openMsgId": "msg-2", "occurredAt": float64(20), "text": "latest"},
			},
		},
	}
}

func TestDispatchRobotLifecycleAddsRecallsThenRepliesExactlyOnce(t *testing.T) {
	recorder, server := newDispatchRobotServer(t)
	taskID := typingTestUUID(11)
	workspaceID := typingTestUUID(12)
	agentID := typingTestUUID(13)
	result, err := json.Marshal(protocol.TaskCompletedPayload{TaskID: util.UUIDToString(taskID), Output: "done"})
	if err != nil {
		t.Fatal(err)
	}
	queries := &dispatchLifecycleQueries{
		inst: testInstallationRow(t, typingTestUUID(14), "client_a"),
		task: db.AgentTaskQueue{ID: taskID, Result: result},
	}
	outbound := NewOutbound(queries, plaintextDecrypter, NewRobotMessenger(server.URL, server.URL, server.Client()), nil, nil)
	payload := dispatchLifecyclePayload(util.UUIDToString(taskID), util.UUIDToString(workspaceID), util.UUIDToString(agentID), "group")
	payload["dispatch_source"] = map[string]any{"platform": "dingtalk", "type": "digital_employee"}

	for range 2 {
		if err := outbound.processEvent(context.Background(), events.Event{Type: protocol.EventTaskQueued, Payload: payload}); err != nil {
			t.Fatalf("queued lifecycle: %v", err)
		}
	}
	for range 2 {
		if err := outbound.processEvent(context.Background(), events.Event{Type: protocol.EventTaskCompleted, Payload: payload}); err != nil {
			t.Fatalf("completed lifecycle: %v", err)
		}
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	wantSequence := []string{
		"/v1.0/robot/emotion/reply",
		"/v1.0/robot/emotion/recall",
		"/v1.0/robot/groupMessages/send",
	}
	if len(recorder.sequence) != len(wantSequence) {
		t.Fatalf("DingTalk lifecycle calls = %v, want %v", recorder.sequence, wantSequence)
	}
	for i := range wantSequence {
		if recorder.sequence[i] != wantSequence[i] {
			t.Fatalf("DingTalk lifecycle calls = %v, want %v", recorder.sequence, wantSequence)
		}
	}
	for _, path := range wantSequence[:2] {
		body := recorder.bodies[path][0]
		if body["openConversationId"] != "cid-1" || body["openMsgId"] != "msg-2" {
			t.Fatalf("%s target = %#v, want latest message", path, body)
		}
	}
	reply := recorder.bodies["/v1.0/robot/groupMessages/send"][0]
	if reply["openConversationId"] != "cid-1" || reply["openMsgId"] != "msg-2" {
		t.Fatalf("reply target = %#v, want latest group message", reply)
	}
}

func TestDispatchRobotLifecycleLeavesChatTasksToChatOutbound(t *testing.T) {
	recorder, server := newDispatchRobotServer(t)
	taskID := typingTestUUID(31)
	queries := &dispatchLifecycleQueries{
		inst: testInstallationRow(t, typingTestUUID(32), "client_c"),
		task: db.AgentTaskQueue{ID: taskID},
	}
	outbound := NewOutbound(queries, plaintextDecrypter, NewRobotMessenger(server.URL, server.URL, server.Client()), nil, nil)
	payload := dispatchLifecyclePayload(util.UUIDToString(taskID), util.UUIDToString(typingTestUUID(33)), util.UUIDToString(typingTestUUID(34)), "group")
	delete(payload, "issue_id")
	payload["chat_session_id"] = util.UUIDToString(typingTestUUID(35))

	if err := outbound.processEvent(context.Background(), events.Event{Type: protocol.EventTaskQueued, Payload: payload}); err != nil {
		t.Fatalf("queued chat lifecycle: %v", err)
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.sequence) != 0 {
		t.Fatalf("chat task used issue outbound lifecycle: %v", recorder.sequence)
	}
}

func TestDispatchRobotFailureRecallsAndRepliesToPrivateSender(t *testing.T) {
	recorder, server := newDispatchRobotServer(t)
	taskID := typingTestUUID(21)
	workspaceID := typingTestUUID(22)
	agentID := typingTestUUID(23)
	queries := &dispatchLifecycleQueries{
		inst: testInstallationRow(t, typingTestUUID(24), "client_b"),
		task: db.AgentTaskQueue{ID: taskID},
	}
	outbound := NewOutbound(queries, plaintextDecrypter, NewRobotMessenger(server.URL, server.URL, server.Client()), nil, nil)
	payload := dispatchLifecyclePayload(util.UUIDToString(taskID), util.UUIDToString(workspaceID), util.UUIDToString(agentID), "single")

	if err := outbound.processEvent(context.Background(), events.Event{Type: protocol.EventTaskQueued, Payload: payload}); err != nil {
		t.Fatalf("queued lifecycle: %v", err)
	}
	for range 2 {
		if err := outbound.processEvent(context.Background(), events.Event{Type: protocol.EventTaskFailed, Payload: payload}); err != nil {
			t.Fatalf("failed lifecycle: %v", err)
		}
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	wantSequence := []string{
		"/v1.0/robot/emotion/reply",
		"/v1.0/robot/emotion/recall",
		"/v1.0/robot/oToMessages/batchSend",
	}
	if len(recorder.sequence) != len(wantSequence) {
		t.Fatalf("DingTalk failure lifecycle calls = %v, want %v", recorder.sequence, wantSequence)
	}
	for i := range wantSequence {
		if recorder.sequence[i] != wantSequence[i] {
			t.Fatalf("DingTalk failure lifecycle calls = %v, want %v", recorder.sequence, wantSequence)
		}
	}
	recall := recorder.bodies["/v1.0/robot/emotion/recall"][0]
	if recall["openMsgId"] != "msg-2" {
		t.Fatalf("failure recall target = %#v, want latest message", recall)
	}
	reply := recorder.bodies["/v1.0/robot/oToMessages/batchSend"][0]
	users, _ := reply["userIds"].([]any)
	if len(users) != 1 || users[0] != "staff-1" {
		t.Fatalf("private reply target = %#v, want staff-1", reply)
	}
	msgParam, _ := reply["msgParam"].(string)
	if !strings.Contains(msgParam, "处理失败") {
		t.Fatalf("private failure reply = %q", msgParam)
	}
}

func TestDispatchRobotStreamEmotionOwnsLifecycleAndPostsLastReply(t *testing.T) {
	recorder, server := newDispatchRobotServer(t)
	taskID := typingTestUUID(51)
	workspaceID := typingTestUUID(52)
	agentID := typingTestUUID(53)
	inst := testInstallationRow(t, typingTestUUID(54), "client_stream")
	streamQ := newFakeStreamEmotionQueries(inst)
	queries := &dispatchLifecycleQueries{
		inst:      inst,
		task:      db.AgentTaskQueue{ID: taskID},
		stream:    streamQ,
		lastReply: "今日要闻：预发环境没有外网，查到的是本地摘要。",
	}
	messenger := NewRobotMessenger(server.URL, server.URL, server.Client())
	mgr := NewTypingIndicatorManager(messenger, plaintextDecrypter, streamQ, nil)
	mgr.beginStreamEmotion(context.Background(), inst.ID, "msg-2", EmotionTarget{
		OpenConversationID: "cid-1", OpenMsgID: "msg-2", RobotCode: "robot_client_stream",
	}, 0)
	outbound := NewOutbound(queries, plaintextDecrypter, messenger, mgr, nil)
	parentPayload := dispatchLifecyclePayload(util.UUIDToString(taskID), util.UUIDToString(workspaceID), util.UUIDToString(agentID), "group")
	childPayload := dispatchLifecyclePayload(util.UUIDToString(typingTestUUID(55)), util.UUIDToString(workspaceID), util.UUIDToString(agentID), "group")
	childPayload["dispatch_idempotency_key"] = "dispatch-window:window-1:retry"

	if err := outbound.processEvent(context.Background(), events.Event{Type: protocol.EventTaskQueued, Payload: parentPayload}); err != nil {
		t.Fatalf("parent queued: %v", err)
	}
	parentPayload["retry_pending"] = true
	if err := outbound.processEvent(context.Background(), events.Event{Type: protocol.EventTaskFailed, Payload: parentPayload}); err != nil {
		t.Fatalf("parent retry-pending failed: %v", err)
	}
	if err := outbound.processEvent(context.Background(), events.Event{Type: protocol.EventTaskQueued, Payload: childPayload}); err != nil {
		t.Fatalf("child queued: %v", err)
	}
	if err := outbound.processEvent(context.Background(), events.Event{Type: protocol.EventTaskCompleted, Payload: childPayload}); err != nil {
		t.Fatalf("child completed: %v", err)
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	wantSequence := []string{
		"/v1.0/robot/emotion/reply",
		"/v1.0/robot/emotion/recall",
		"/v1.0/robot/groupMessages/send",
	}
	if len(recorder.sequence) != len(wantSequence) {
		t.Fatalf("stream-owned DingTalk calls = %v, want %v", recorder.sequence, wantSequence)
	}
	for i := range wantSequence {
		if recorder.sequence[i] != wantSequence[i] {
			t.Fatalf("stream-owned DingTalk calls = %v, want %v", recorder.sequence, wantSequence)
		}
	}
	reply := recorder.bodies["/v1.0/robot/groupMessages/send"][0]
	msgParam, _ := reply["msgParam"].(string)
	if !strings.Contains(msgParam, "今日要闻") {
		t.Fatalf("robot completion reply = %q, want last agent comment", msgParam)
	}
}

func TestDispatchRobotRetryPendingFailureStaysSilent(t *testing.T) {
	recorder, server := newDispatchRobotServer(t)
	taskID := typingTestUUID(61)
	queries := &dispatchLifecycleQueries{
		inst: testInstallationRow(t, typingTestUUID(62), "client_retry"),
		task: db.AgentTaskQueue{ID: taskID},
	}
	outbound := NewOutbound(queries, plaintextDecrypter, NewRobotMessenger(server.URL, server.URL, server.Client()), nil, nil)
	payload := dispatchLifecyclePayload(util.UUIDToString(taskID), util.UUIDToString(typingTestUUID(63)), util.UUIDToString(typingTestUUID(64)), "group")

	if err := outbound.processEvent(context.Background(), events.Event{Type: protocol.EventTaskQueued, Payload: payload}); err != nil {
		t.Fatalf("queued lifecycle: %v", err)
	}
	payload["retry_pending"] = true
	if err := outbound.processEvent(context.Background(), events.Event{Type: protocol.EventTaskFailed, Payload: payload}); err != nil {
		t.Fatalf("retry-pending failed: %v", err)
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.sequence) != 1 || recorder.sequence[0] != "/v1.0/robot/emotion/reply" {
		t.Fatalf("retry-pending DingTalk calls = %v, want one processing emotion", recorder.sequence)
	}
}

func TestStreamIssueCompletionPostsLastReplyAndRecallsEmotion(t *testing.T) {
	recorder, server := newDispatchRobotServer(t)
	taskID := typingTestUUID(71)
	sessionID := typingTestUUID(72)
	inst := testInstallationRow(t, typingTestUUID(73), "client_stream_issue")
	streamQ := newFakeStreamEmotionQueries(inst)
	queries := &dispatchLifecycleQueries{
		inst: inst,
		binding: db.ChannelChatSessionBinding{
			ChatSessionID: sessionID,
			ChatType:      string(channel.ChatTypeP2P),
			Config:        []byte(`{"sender_staff_id":"staff-1"}`),
		},
		task:      db.AgentTaskQueue{ID: taskID},
		stream:    streamQ,
		lastReply: "今天要闻：预发环境验证用的三条摘要。",
	}
	messenger := NewRobotMessenger(server.URL, server.URL, server.Client())
	mgr := NewTypingIndicatorManager(messenger, plaintextDecrypter, streamQ, nil)
	mgr.beginStreamEmotion(context.Background(), inst.ID, "msg-stream", EmotionTarget{
		OpenConversationID: "cid-dm", OpenMsgID: "msg-stream", RobotCode: "robot_client_stream_issue",
	}, 0)
	mgr.bindStreamEmotion(context.Background(), inst.ID, "msg-stream", sessionID, taskID)
	outbound := NewOutbound(queries, plaintextDecrypter, messenger, mgr, nil)
	payload := map[string]any{
		"task_id":  util.UUIDToString(taskID),
		"issue_id": "11111111-1111-1111-1111-111111111111",
	}

	if err := outbound.processEvent(context.Background(), events.Event{Type: protocol.EventTaskQueued, Payload: payload}); err != nil {
		t.Fatalf("stream issue queued: %v", err)
	}
	if err := outbound.processEvent(context.Background(), events.Event{Type: protocol.EventTaskCompleted, Payload: payload}); err != nil {
		t.Fatalf("stream issue completed: %v", err)
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	wantSequence := []string{
		"/v1.0/robot/emotion/reply",
		"/v1.0/robot/emotion/recall",
		"/v1.0/robot/oToMessages/batchSend",
	}
	if len(recorder.sequence) != len(wantSequence) {
		t.Fatalf("stream issue DingTalk calls = %v, want %v", recorder.sequence, wantSequence)
	}
	for i := range wantSequence {
		if recorder.sequence[i] != wantSequence[i] {
			t.Fatalf("stream issue DingTalk calls = %v, want %v", recorder.sequence, wantSequence)
		}
	}
	reply := recorder.bodies["/v1.0/robot/oToMessages/batchSend"][0]
	users, _ := reply["userIds"].([]any)
	if len(users) != 1 || users[0] != "staff-1" {
		t.Fatalf("stream issue reply target = %#v, want staff-1", reply)
	}
	msgParam, _ := reply["msgParam"].(string)
	if !strings.Contains(msgParam, "今天要闻") {
		t.Fatalf("stream issue reply = %q, want last agent comment", msgParam)
	}
}

func TestStreamIssueRetryPendingFailureStaysSilent(t *testing.T) {
	recorder, server := newDispatchRobotServer(t)
	taskID := typingTestUUID(81)
	sessionID := typingTestUUID(82)
	inst := testInstallationRow(t, typingTestUUID(83), "client_stream_retry")
	streamQ := newFakeStreamEmotionQueries(inst)
	queries := &dispatchLifecycleQueries{
		inst: inst,
		binding: db.ChannelChatSessionBinding{
			ChatSessionID: sessionID,
			ChatType:      string(channel.ChatTypeP2P),
			Config:        []byte(`{"sender_staff_id":"staff-1"}`),
		},
		task:   db.AgentTaskQueue{ID: taskID},
		stream: streamQ,
	}
	messenger := NewRobotMessenger(server.URL, server.URL, server.Client())
	mgr := NewTypingIndicatorManager(messenger, plaintextDecrypter, streamQ, nil)
	mgr.beginStreamEmotion(context.Background(), inst.ID, "msg-retry", EmotionTarget{
		OpenConversationID: "cid-dm", OpenMsgID: "msg-retry", RobotCode: "robot_client_stream_retry",
	}, 0)
	mgr.bindStreamEmotion(context.Background(), inst.ID, "msg-retry", sessionID, taskID)
	outbound := NewOutbound(queries, plaintextDecrypter, messenger, mgr, nil)
	payload := map[string]any{
		"task_id":       util.UUIDToString(taskID),
		"issue_id":      "11111111-1111-1111-1111-111111111111",
		"retry_pending": true,
	}
	if err := outbound.processEvent(context.Background(), events.Event{Type: protocol.EventTaskFailed, Payload: payload}); err != nil {
		t.Fatalf("stream issue retry-pending failed: %v", err)
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.sequence) != 1 || recorder.sequence[0] != "/v1.0/robot/emotion/reply" {
		t.Fatalf("stream issue retry-pending calls = %v, want one processing emotion", recorder.sequence)
	}
}
