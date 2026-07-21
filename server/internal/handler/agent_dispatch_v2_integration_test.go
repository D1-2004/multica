package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestHandleAgentDispatchV2CreatesSafeIssueWithoutRequestIdentity(t *testing.T) {
	agentID := createHandlerTestAgent(t, "test-v2-safe-issue", nil)
	body := fmt.Sprintf(`{
		"schemaVersion":"2.0",
		"agentId":%q,
		"continuation":null,
		"source":{"platform":"dingtalk","type":"digital_employee"},
		"event":{
			"domain":"channel",
			"type":"message.created",
			"data":{
				"conversation":{"openConversationId":"cid-private","type":"single","title":"项目群"},
				"sender":{"displayName":"张三"},
				"messages":[
					{"openMsgId":"msg-private-1","occurredAt":1784512800000,"text":"帮我看一下线上告警"},
					{"openMsgId":"msg-private-2","occurredAt":1784512801800,"text":"关注最近十分钟的错误日志"}
				]
			}
		},
		"surface":{"type":"issue"},
		"outbound":{"mode":"dws","replyTo":"latest_message"}
	}`, agentID)

	w := postAgentDispatchForTest(t, body, agentID)
	if w.Code != http.StatusCreated {
		t.Fatalf("HandleAgentDispatch v2: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var response AgentDispatchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, response.Continuation.IssueID)
	})

	var title string
	var description pgtype.Text
	if err := testPool.QueryRow(context.Background(), `
		SELECT title, description
		FROM issue
		WHERE id = $1
	`, response.Continuation.IssueID).Scan(&title, &description); err != nil {
		t.Fatal(err)
	}
	if title != "帮我看一下线上告警" {
		t.Fatalf("title = %q", title)
	}
	for _, visible := range []string{"张三", "帮我看一下线上告警", "关注最近十分钟的错误日志"} {
		if !strings.Contains(description.String, visible) {
			t.Errorf("description missing %q: %s", visible, description.String)
		}
	}
	for _, private := range []string{"cid-private", "msg-private-1", "msg-private-2", "DWS", "untrusted", "System prompt", "User prompt"} {
		if strings.Contains(description.String, private) {
			t.Errorf("description leaked %q: %s", private, description.String)
		}
	}

	var taskContext []byte
	var hasIdentityToken bool
	if err := testPool.QueryRow(context.Background(), `
		SELECT context, COALESCE(context ? 'agent_identity_context_token', false)
		FROM agent_task_queue
		WHERE id = $1
	`, response.TaskID).Scan(&taskContext, &hasIdentityToken); err != nil {
		t.Fatal(err)
	}
	if hasIdentityToken {
		t.Fatalf("identity-less dispatch fabricated a context token: %s", taskContext)
	}
	for _, structuredValue := range []string{"dispatch_schema_version", "dispatch_event_data", "cid-private", "msg-private-2", "dispatch_outbound", "latest_message"} {
		if !strings.Contains(string(taskContext), structuredValue) {
			t.Errorf("task structured context missing %q: %s", structuredValue, taskContext)
		}
	}
	for _, generatedPromptField := range []string{"dispatch_runtime_prompt", "dispatch_workflow_prompt"} {
		if strings.Contains(string(taskContext), generatedPromptField) {
			t.Errorf("task context persisted generated prompt field %q: %s", generatedPromptField, taskContext)
		}
	}

	var runtimeID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT runtime_id
		FROM agent_task_queue
		WHERE id = $1
	`, response.TaskID).Scan(&runtimeID); err != nil {
		t.Fatal(err)
	}
	claimW := httptest.NewRecorder()
	claimReq := newDaemonTokenRequest(
		http.MethodPost,
		"/api/daemon/runtimes/"+runtimeID+"/tasks/claim",
		map[string]any{"target_task_id": response.TaskID},
		testWorkspaceID,
		"dispatch-v2-claim",
	)
	claimReq = withURLParam(claimReq, "runtimeId", runtimeID)
	testHandler.ClaimTaskByRuntime(claimW, claimReq)
	if claimW.Code != http.StatusOK {
		t.Fatalf("ClaimTaskByRuntime: expected 200, got %d: %s", claimW.Code, claimW.Body.String())
	}
	var claim struct {
		Task *struct {
			ID          string `json:"id"`
			HandoffNote string `json:"handoff_note"`
		} `json:"task"`
	}
	if err := json.Unmarshal(claimW.Body.Bytes(), &claim); err != nil {
		t.Fatal(err)
	}
	if claim.Task == nil || claim.Task.ID != response.TaskID {
		t.Fatalf("claim returned wrong task: %s", claimW.Body.String())
	}
	for _, required := range []string{
		"## Trusted DingTalk Dispatch",
		`"openConversationId":"cid-private"`,
		`"openMsgId":"msg-private-2"`,
		"dws chat message add-emoji",
		"at most 4 visible characters",
		"dws chat message reply",
	} {
		if !strings.Contains(claim.Task.HandoffNote, required) {
			t.Errorf("claim handoff_note missing %q: %s", required, claim.Task.HandoffNote)
		}
	}
	for _, forbidden := range []string{"dispatch_runtime_prompt", "dispatch_workflow_prompt", "dispatch_surface_type", "dispatch_outbound_mode"} {
		if strings.Contains(claimW.Body.String(), forbidden) {
			t.Errorf("claim response introduced dispatch wire field %q: %s", forbidden, claimW.Body.String())
		}
	}
}

func TestHandleAgentDispatchV2AcceptsRobotChatSurface(t *testing.T) {
	agentID := createHandlerTestAgent(t, "test-v2-robot-chat", nil)
	body := fmt.Sprintf(`{
		"schemaVersion":"2.0",
		"agentId":%q,
		"continuation":null,
		"source":{"platform":"dingtalk","type":"robot"},
		"event":{
			"domain":"channel",
			"type":"message.created",
			"data":{
				"conversation":{"openConversationId":"cid-robot","type":"group","title":"机器人群"},
				"sender":{"displayName":"张三","openDingTalkId":"open-sender"},
				"messages":[{"openMsgId":"msg-robot","occurredAt":1784512800000,"text":"处理机器人消息"}]
			}
		},
		"surface":{"type":"chat"},
		"outbound":{"mode":"robot_sdk","replyTo":"latest_message"}
	}`, agentID)

	w := postAgentDispatchForTest(t, body, agentID)
	if w.Code != http.StatusCreated {
		t.Fatalf("HandleAgentDispatch v2 robot/chat: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var response AgentDispatchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, response.Continuation.IssueID)
	})

	var surfaceType string
	if err := testPool.QueryRow(context.Background(), `
		SELECT context->'dispatch_surface'->>'type'
		FROM agent_task_queue
		WHERE id = $1
	`, response.TaskID).Scan(&surfaceType); err != nil {
		t.Fatal(err)
	}
	if surfaceType != "chat" {
		t.Fatalf("dispatch surface type = %q, want chat", surfaceType)
	}
}
