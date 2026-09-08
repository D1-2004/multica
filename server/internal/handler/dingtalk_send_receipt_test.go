package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (f *dingTalkResponseFixture) taskToken(t *testing.T, taskID, agentID, workspaceID string) string {
	t.Helper()
	token := "mat_" + uuid.NewString()
	row, err := f.h.Queries.CreateTaskToken(context.Background(), db.CreateTaskTokenParams{
		TokenHash: auth.HashToken(token), TaskID: parseUUID(taskID), AgentID: parseUUID(agentID), WorkspaceID: parseUUID(workspaceID),
		UserID: parseUUID(testUserID), ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM task_token WHERE id=$1`, row.ID) })
	return token
}

func (f *dingTalkResponseFixture) receiptRouter() http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.Auth(f.h.Queries, nil, nil))
	router.Post("/api/tasks/{taskID}/dingtalk-send-receipts", f.h.RecordDingTalkSendReceipt)
	return router
}

func receiptRequest(t *testing.T, handler http.Handler, token, pathTaskID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+pathTaskID+"/dingtalk-send-receipts", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	// These headers are deliberately forged. Auth must replace them with the token scope.
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", "forged-agent")
	req.Header.Set("X-Task-ID", pathTaskID)
	req.Header.Set("X-Workspace-ID", "forged-workspace")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func validReceiptBody() protocol.DingTalkSendReceipt {
	return protocol.DingTalkSendReceipt{ClientActionID: "client-send-1", State: "pending", PayloadHash: strings.Repeat("a", 64), OpenConversationID: "cid-outbound"}
}

func TestDingTalkSendReceiptUsesAuthenticatedTaskScope(t *testing.T) {
	f := newDingTalkResponseFixture(t, testRouterTargetIdentity)
	other := newDingTalkResponseFixture(t, testRouterTargetIdentity)
	var otherWorkspaceID string
	if err := testPool.QueryRow(context.Background(), `INSERT INTO workspace(name,slug,description,issue_prefix) VALUES('Receipt scope test',$1,'','RCP') RETURNING id`, "receipt-scope-"+uuid.NewString()).Scan(&otherWorkspaceID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id=$1`, otherWorkspaceID)
	})
	router := f.receiptRouter()
	for _, tc := range []struct {
		name, taskID, agentID, workspaceID, path string
		want                                     int
	}{
		{"same task", f.taskID, f.agentID, testWorkspaceID, f.taskID, http.StatusOK},
		{"other task", other.taskID, other.agentID, testWorkspaceID, f.taskID, http.StatusForbidden},
		{"other agent", f.taskID, other.agentID, testWorkspaceID, f.taskID, http.StatusNotFound},
		{"other workspace", f.taskID, f.agentID, otherWorkspaceID, f.taskID, http.StatusNotFound},
		{"path not token task", f.taskID, f.agentID, testWorkspaceID, other.taskID, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := f.taskToken(t, tc.taskID, tc.agentID, tc.workspaceID)
			w := receiptRequest(t, router, token, tc.path, validReceiptBody())
			if w.Code != tc.want {
				t.Fatalf("got %d want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
	pat := "mul_" + uuid.NewString()
	row, err := f.h.Queries.CreatePersonalAccessToken(context.Background(), db.CreatePersonalAccessTokenParams{
		UserID: parseUUID(testUserID), Name: "receipt-test", TokenHash: auth.HashToken(pat), TokenPrefix: "mul_test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM personal_access_token WHERE id=$1`, row.ID)
	})
	if w := receiptRequest(t, router, pat, f.taskID, validReceiptBody()); w.Code != http.StatusForbidden {
		t.Fatalf("PAT forged task source: %d %s", w.Code, w.Body.String())
	}
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM sandbox_send_receipt WHERE agent_id=$1`, f.agentID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rejected scope inserted receipts: %d", count)
	}
}

func TestDingTalkSendReceiptRejectsLegacyAndOtherDeliveryModes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*DispatchCommand)
	}{
		{"legacy", func(c *DispatchCommand) { c.ResponsePolicy = nil }},
		{"robot", func(c *DispatchCommand) { c.Source.Type = "robot" }},
		{"robot SDK", func(c *DispatchCommand) { c.Outbound.Mode = "robot_sdk" }},
		{"reaction", func(c *DispatchCommand) { c.Event.Type = "emotionReply" }},
		{"cancel", func(c *DispatchCommand) { c.Control = &DispatchControl{Action: "cancel"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDingTalkResponseFixture(t, testRouterTargetIdentity)
			tc.mutate(&f.command)
			f.setContext(t, dispatchRuntimeContext(f.command, "scope"))
			token := f.taskToken(t, f.taskID, f.agentID, testWorkspaceID)
			w := receiptRequest(t, f.receiptRouter(), token, f.taskID, validReceiptBody())
			if w.Code != http.StatusForbidden {
				t.Fatalf("out-of-scope receipt accepted: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestDingTalkSendReceiptDoesNotTrustClientDeliveryClaim(t *testing.T) {
	f := newDingTalkResponseFixture(t, testRouterTargetIdentity)
	token := f.taskToken(t, f.taskID, f.agentID, testWorkspaceID)
	body := validReceiptBody()
	body.State, body.OpenTaskID, body.OpenMessageID = "delivered", "provider-task-1", "unverified-message"
	w := receiptRequest(t, f.receiptRouter(), token, f.taskID, body)
	if w.Code != http.StatusOK {
		t.Fatalf("receipt rejected: %d %s", w.Code, w.Body.String())
	}
	var state, message string
	if err := testPool.QueryRow(context.Background(), `SELECT state,provider_message_id FROM sandbox_send_receipt WHERE task_id=$1`, f.taskID).Scan(&state, &message); err != nil {
		t.Fatal(err)
	}
	if state != "provider_accepted" || message != "" {
		t.Fatalf("client fabricated delivery evidence: state=%s message=%q", state, message)
	}
	body.State, body.OpenTaskID, body.OpenMessageID = "pending", "", ""
	w = receiptRequest(t, f.receiptRouter(), token, f.taskID, body)
	if w.Code != http.StatusOK {
		t.Fatalf("late pending observation rejected: %d %s", w.Code, w.Body.String())
	}
	if err := testPool.QueryRow(context.Background(), `SELECT state FROM sandbox_send_receipt WHERE task_id=$1`, f.taskID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "provider_accepted" {
		t.Fatalf("late pending erased provider acknowledgement: %s", state)
	}
	body.PayloadHash = strings.Repeat("b", 64)
	w = receiptRequest(t, f.receiptRouter(), token, f.taskID, body)
	if w.Code != http.StatusConflict {
		t.Fatalf("same action with changed payload accepted: %d", w.Code)
	}
}

func TestDingTalkSendReceiptRejectsMalformedPayload(t *testing.T) {
	f := newDingTalkResponseFixture(t, testRouterTargetIdentity)
	token := f.taskToken(t, f.taskID, f.agentID, testWorkspaceID)
	for _, body := range []any{
		map[string]any{"clientActionId": "id", "state": "pending", "payloadHash": strings.Repeat("x", 64)},
		map[string]any{"clientActionId": "id", "state": "pending", "payloadHash": strings.Repeat("a", 64), "agentId": uuid.NewString()},
		map[string]any{"clientActionId": "id", "state": "claimed", "payloadHash": strings.Repeat("a", 64)},
	} {
		w := receiptRequest(t, f.receiptRouter(), token, f.taskID, body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("malformed receipt accepted: %d %s", w.Code, w.Body.String())
		}
	}
}
