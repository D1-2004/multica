package handler

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/chattrace"
)

type chatReceiptFixture struct {
	sessionID string
	taskID    string
	messageID string
	trace     chattrace.Trace
}

func createChatReceiptFixture(t *testing.T, role string) chatReceiptFixture {
	t.Helper()
	ctx := context.Background()
	agentID, sessionID, runtimeID, _ := setupDirectChatSession(t, ctx, "chat receipt")
	trace, err := chattrace.From("37d0871a-3657-4c74-91fa-39e846fa90a0", "web", time.Now().Add(-2*time.Second).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	taskContext, err := chattrace.Merge(nil, trace)
	if err != nil {
		t.Fatal(err)
	}
	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, status, priority, chat_session_id, context,
			initiator_user_id, originator_user_id, dispatched_at, started_at, completed_at
		)
		VALUES ($1, $2, 'completed', 2, $3, $4::jsonb, $5, $5, now(), now(), now())
		RETURNING id
	`, agentID, runtimeID, sessionID, taskContext, testUserID).Scan(&taskID); err != nil {
		t.Fatalf("insert receipt task: %v", err)
	}
	var messageID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_message (chat_session_id, role, content, task_id)
		VALUES ($1, $2, 'receipt fixture', $3)
		RETURNING id
	`, sessionID, role, taskID).Scan(&messageID); err != nil {
		t.Fatalf("insert receipt message: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM chat_message WHERE id = $1`, messageID)
		testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})
	return chatReceiptFixture{sessionID: sessionID, taskID: taskID, messageID: messageID, trace: trace}
}

func chatReceiptRequest(t *testing.T, fixture chatReceiptFixture, overrides map[string]any) *http.Request {
	t.Helper()
	wsAt := time.UnixMilli(fixture.trace.StartedAtUnixMS).Add(time.Second)
	renderedAt := wsAt.Add(250 * time.Millisecond)
	clientReceivedAt := renderedAt.Add(25 * time.Millisecond)
	body := map[string]any{
		"task_id":                fixture.taskID,
		"trace_id":               fixture.trace.TraceID,
		"ws_received_at_unix_ms": wsAt.UnixMilli(),
		"rendered_at_unix_ms":    renderedAt.UnixMilli(),
		"client_received_at":     clientReceivedAt.UTC().Format(time.RFC3339Nano),
		"elapsed_ms":             renderedAt.Sub(time.UnixMilli(fixture.trace.StartedAtUnixMS)).Milliseconds(),
	}
	for key, value := range overrides {
		body[key] = value
	}
	req := newRequest(http.MethodPost, "/api/chat/sessions/"+fixture.sessionID+"/messages/"+fixture.messageID+"/received", body)
	req = withURLParam(req, "sessionId", fixture.sessionID)
	chi.RouteContext(req.Context()).URLParams.Add("messageId", fixture.messageID)
	return withChatTestWorkspaceCtx(t, req)
}

func TestAcknowledgeChatMessageReceived(t *testing.T) {
	fixture := createChatReceiptFixture(t, "assistant")
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	w := httptest.NewRecorder()
	testHandler.AcknowledgeChatMessageReceived(w, chatReceiptRequest(t, fixture, nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("receipt status = %d body=%s", w.Code, w.Body.String())
	}
	for _, field := range []string{
		`"event":"chat_trace_stage"`,
		`"stage":"web_client_received"`,
		`"client_reported_ws_received_at_unix_ms"`,
		`"client_reported_rendered_at_unix_ms"`,
		`"client_reported_client_received_at"`,
		`"client_reported_elapsed_ms"`,
		`"server_receipt_at"`,
		`"client_platform"`,
		`"client_version"`,
		`"client_os"`,
	} {
		if !strings.Contains(logs.String(), field) {
			t.Fatalf("receipt log missing %s: %s", field, logs.String())
		}
	}

	duplicate := httptest.NewRecorder()
	testHandler.AcknowledgeChatMessageReceived(duplicate, chatReceiptRequest(t, fixture, nil))
	if duplicate.Code != http.StatusNoContent {
		t.Fatalf("duplicate receipt status = %d body=%s", duplicate.Code, duplicate.Body.String())
	}
	if count := strings.Count(logs.String(), `"stage":"web_client_received"`); count != 1 {
		t.Fatalf("receipt log count = %d, logs=%s", count, logs.String())
	}
	var recorded bool
	if err := testPool.QueryRow(context.Background(), `
		SELECT client_receipt_recorded_at IS NOT NULL
		FROM chat_message
		WHERE id = $1
	`, fixture.messageID).Scan(&recorded); err != nil {
		t.Fatalf("read receipt marker: %v", err)
	}
	if !recorded {
		t.Fatal("receipt marker was not persisted")
	}
}

func TestAcknowledgeChatMessageReceivedRejectsMismatches(t *testing.T) {
	fixture := createChatReceiptFixture(t, "assistant")
	for name, overrides := range map[string]map[string]any{
		"task":  {"task_id": "00000000-0000-0000-0000-000000000099"},
		"trace": {"trace_id": "00000000-0000-0000-0000-000000000098"},
	} {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			testHandler.AcknowledgeChatMessageReceived(w, chatReceiptRequest(t, fixture, overrides))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestAcknowledgeChatMessageReceivedRejectsNonAssistantAndCrossSession(t *testing.T) {
	nonAssistant := createChatReceiptFixture(t, "user")
	w := httptest.NewRecorder()
	testHandler.AcknowledgeChatMessageReceived(w, chatReceiptRequest(t, nonAssistant, nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("non-assistant status = %d body=%s", w.Code, w.Body.String())
	}

	assistant := createChatReceiptFixture(t, "assistant")
	_, otherSessionID, _, _ := setupDirectChatSession(t, context.Background(), "other receipt session")
	cross := assistant
	cross.sessionID = otherSessionID
	w = httptest.NewRecorder()
	testHandler.AcknowledgeChatMessageReceived(w, chatReceiptRequest(t, cross, nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-session status = %d body=%s", w.Code, w.Body.String())
	}
}
