package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	for _, privateRuntimeValue := range []string{"dispatch_runtime_prompt", "DWS", "dispatch_outbound", "latest_message"} {
		if !strings.Contains(string(taskContext), privateRuntimeValue) {
			t.Errorf("task private context missing %q: %s", privateRuntimeValue, taskContext)
		}
	}
}
