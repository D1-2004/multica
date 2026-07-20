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
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type dispatchLifecycleQueries struct {
	inst db.ChannelInstallation
	task db.AgentTaskQueue

	mu     sync.Mutex
	claims map[string]bool
}

func (q *dispatchLifecycleQueries) GetChannelChatSessionBindingBySession(context.Context, db.GetChannelChatSessionBindingBySessionParams) (db.ChannelChatSessionBinding, error) {
	return db.ChannelChatSessionBinding{}, pgx.ErrNoRows
}

func (q *dispatchLifecycleQueries) GetChannelInstallation(context.Context, db.GetChannelInstallationParams) (db.ChannelInstallation, error) {
	return db.ChannelInstallation{}, pgx.ErrNoRows
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
		"task_id":      taskID,
		"workspace_id": workspaceID,
		"agent_id":     agentID,
		"dispatch_idempotency_key": "dispatch-window:window-1",
		"dispatch_source": map[string]any{"platform": "dingtalk", "type": "robot"},
		"dispatch_outbound": map[string]any{"mode": "robot_sdk", "replyTo": "latest_message"},
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
