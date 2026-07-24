package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
)

func TestHandleAgentDispatchV2CreatesSafeIssueWithoutRequestIdentity(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

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
	logOutput := logs.String()
	for _, field := range []string{
		"MULTICA_AGENT_DISPATCH_REQUEST",
		"outcome=validated",
		"outcome=created_issue",
		"protocol=dispatch_command_v2",
		"schemaVersion=2.0",
		"eventType=message.created",
		"messageCount=2",
		"promptBuilder=multica",
	} {
		if !strings.Contains(logOutput, field) {
			t.Fatalf("dispatch request log missing %q: %s", field, logOutput)
		}
	}
	for _, private := range []string{"帮我看一下线上告警", "cid-private", "msg-private-1"} {
		if strings.Contains(logOutput, private) {
			t.Fatalf("dispatch request log leaked %q: %s", private, logOutput)
		}
	}
	var response AgentDispatchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logOutput, response.Continuation.IssueID) || strings.Contains(logOutput, response.TaskID) {
		t.Fatalf("dispatch request log leaked issue or task id: %s", logOutput)
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

func TestHandleAgentDispatchV2RecreatesMissingContinuationIssue(t *testing.T) {
	agentID := createHandlerTestAgent(t, "test-v2-missing-continuation", nil)
	const missingIssueID = "00000000-0000-4000-8000-000000000002"
	body := fmt.Sprintf(`{
		"schemaVersion":"2.0",
		"agentId":%q,
		"continuation":{"kind":"issue","issueId":%q},
		"source":{"platform":"dingtalk","type":"digital_employee"},
		"event":{
			"domain":"channel",
			"type":"message.created",
			"data":{
				"conversation":{"openConversationId":"cid-recreated","type":"single"},
				"sender":{"displayName":"张三"},
				"messages":[{"openMsgId":"msg-recreated","occurredAt":1784512800000,"text":"原续接 Issue 已删除，请继续处理"}]
			}
		},
		"surface":{"type":"issue"},
		"outbound":{"mode":"dws","replyTo":"latest_message"}
	}`, agentID, missingIssueID)

	w := postAgentDispatchForTest(t, body, agentID)
	if w.Code != http.StatusCreated {
		t.Fatalf("HandleAgentDispatch v2 missing continuation: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var response AgentDispatchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.TaskID == "" || response.Continuation.Kind != "issue" || response.Continuation.IssueID == "" {
		t.Fatalf("unexpected response: %+v", response)
	}
	if response.Continuation.IssueID == missingIssueID {
		t.Fatalf("continuation issue id was not refreshed: %+v", response.Continuation)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, response.Continuation.IssueID)
	})

	var issueExists bool
	if err := testPool.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM issue WHERE id = $1)`, response.Continuation.IssueID).Scan(&issueExists); err != nil {
		t.Fatalf("check recreated issue: %v", err)
	}
	if !issueExists {
		t.Fatal("recreated continuation issue does not exist")
	}
}

func TestHandleAgentDispatchV2RecoversIssueAfterAcceptanceFinalizeFailure(t *testing.T) {
	agentID := createHandlerTestAgent(t, "test-v2-issue-acceptance-recovery", nil)
	endpointID, deliverySecret := createAgentDispatchEndpointForTest(t, testUserID, agentID)
	const idempotencyKey = "issue-acceptance-recovery"
	body := fmt.Sprintf(`{
		"schemaVersion":"2.0",
		"agentId":%q,
		"continuation":null,
		"completionCallback":{"url":"/api/v1/dispatch-tasks/test-issue-acceptance-recovery/execution-result"},
		"source":{"platform":"dingtalk","type":"digital_employee"},
		"event":{
			"domain":"channel",
			"type":"message.created",
			"data":{
				"conversation":{"openConversationId":"cid-issue-acceptance-recovery","type":"single"},
				"sender":{"displayName":"张三"},
				"messages":[{"openMsgId":"msg-issue-acceptance-recovery","occurredAt":1784512800000,"text":"issue acceptance recovery"}]
			}
		},
		"surface":{"type":"issue"},
		"outbound":{"mode":"dws","replyTo":"latest_message"}
	}`, agentID)
	dispatch := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/webhooks/agent-dispatch", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+deliverySecret)
		req.Header.Set("Idempotency-Key", idempotencyKey)
		req = withURLParams(req, "endpointId", endpointID)
		w := httptest.NewRecorder()
		testHandler.HandleAgentDispatch(w, req)
		return w
	}

	first := dispatch()
	if first.Code != http.StatusCreated {
		t.Fatalf("initial issue dispatch: expected 201, got %d: %s", first.Code, first.Body.String())
	}
	var response AgentDispatchResponse
	if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, response.Continuation.IssueID)
	})
	endpoint, err := testHandler.Queries.GetAgentDispatchEndpointByEndpointID(context.Background(), endpointID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM agent_dispatch_acceptance
			WHERE endpoint_id = $1 AND idempotency_key = $2
		`, endpoint.ID, idempotencyKey)
	})
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_dispatch_acceptance
		SET status = 'pending',
		    response_status = NULL,
		    response_content_type = NULL,
		    response_body = NULL,
		    root_task_id = NULL,
		    lease_token = gen_random_uuid(),
		    lease_expires_at = now() - interval '1 second'
		WHERE endpoint_id = $1 AND idempotency_key = $2
	`, endpoint.ID, idempotencyKey); err != nil {
		t.Fatalf("simulate issue acceptance finalize failure: %v", err)
	}

	replay := dispatch()
	if replay.Code != http.StatusCreated {
		t.Fatalf("recovered issue dispatch: expected 201, got %d: %s", replay.Code, replay.Body.String())
	}
	if !bytes.Equal(replay.Body.Bytes(), first.Body.Bytes()) {
		t.Fatalf("recovered issue response differs:\n got: %s\nwant: %s", replay.Body.Bytes(), first.Body.Bytes())
	}
	var taskCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM agent_task_queue
		WHERE agent_id = $1
		  AND parent_task_id IS NULL
		  AND context #>> '{dispatch_idempotency_key}' = $2
	`, agentID, idempotencyKey).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 1 {
		t.Fatalf("root task count = %d, want 1", taskCount)
	}
}

func TestHandleAgentDispatchV2RecoversIssueFollowUpAfterAcceptanceFinalizeFailure(t *testing.T) {
	agentID := createHandlerTestAgent(t, "test-v2-follow-up-acceptance-recovery", nil)
	var issueID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO issue (
			workspace_id, title, status, priority, assignee_type, assignee_id,
			creator_type, creator_id, number, position
		)
		VALUES (
			$1, 'follow-up acceptance recovery', 'todo', 'none', 'agent', $2,
			'member', $3,
			(SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1),
			0
		)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID).Scan(&issueID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	endpointID, deliverySecret := createAgentDispatchEndpointForTest(t, testUserID, agentID)
	const idempotencyKey = "issue-follow-up-acceptance-recovery"
	body := fmt.Sprintf(`{
		"schemaVersion":"2.0",
		"continuation":{"kind":"issue","issueId":%q},
		"completionCallback":{"url":"/api/v1/dispatch-tasks/test-follow-up-acceptance-recovery/execution-result"},
		"source":{"platform":"dingtalk","type":"digital_employee"},
		"event":{
			"domain":"channel",
			"type":"message.created",
			"data":{
				"conversation":{"openConversationId":"cid-follow-up-acceptance-recovery","type":"single"},
				"sender":{"displayName":"张三"},
				"messages":[{"openMsgId":"msg-follow-up-acceptance-recovery","occurredAt":1784512800000,"text":"continue issue"}]
			}
		},
		"surface":{"type":"issue"},
		"outbound":{"mode":"dws","replyTo":"latest_message"}
	}`, issueID)
	dispatch := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/webhooks/agent-dispatch", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+deliverySecret)
		req.Header.Set("Idempotency-Key", idempotencyKey)
		req = withURLParams(req, "endpointId", endpointID)
		w := httptest.NewRecorder()
		testHandler.HandleAgentDispatch(w, req)
		return w
	}

	first := dispatch()
	if first.Code != http.StatusCreated {
		t.Fatalf("initial follow-up dispatch: expected 201, got %d: %s", first.Code, first.Body.String())
	}
	var response AgentDispatchResponse
	if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.CommentID == "" || response.TaskID == "" {
		t.Fatalf("follow-up response missing identifiers: %+v", response)
	}
	endpoint, err := testHandler.Queries.GetAgentDispatchEndpointByEndpointID(context.Background(), endpointID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM agent_dispatch_acceptance
			WHERE endpoint_id = $1 AND idempotency_key = $2
		`, endpoint.ID, idempotencyKey)
	})
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_dispatch_acceptance
		SET status = 'pending',
		    response_status = NULL,
		    response_content_type = NULL,
		    response_body = NULL,
		    root_task_id = NULL,
		    lease_token = gen_random_uuid(),
		    lease_expires_at = now() - interval '1 second'
		WHERE endpoint_id = $1 AND idempotency_key = $2
	`, endpoint.ID, idempotencyKey); err != nil {
		t.Fatalf("simulate follow-up acceptance finalize failure: %v", err)
	}

	replay := dispatch()
	if replay.Code != http.StatusCreated {
		t.Fatalf("recovered follow-up dispatch: expected 201, got %d: %s", replay.Code, replay.Body.String())
	}
	if !bytes.Equal(replay.Body.Bytes(), first.Body.Bytes()) {
		t.Fatalf("recovered follow-up response differs:\n got: %s\nwant: %s", replay.Body.Bytes(), first.Body.Bytes())
	}
	var taskCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM agent_task_queue
		WHERE agent_id = $1
		  AND parent_task_id IS NULL
		  AND context #>> '{dispatch_idempotency_key}' = $2
	`, agentID, idempotencyKey).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if taskCount != 1 {
		t.Fatalf("follow-up root task count = %d, want 1", taskCount)
	}
}

func TestHandleAgentDispatchV2ReleasesBusyIssueAcceptanceForRetry(t *testing.T) {
	agentID := createHandlerTestAgent(t, "test-v2-busy-acceptance-retry", nil)
	var issueID, runtimeID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO issue (
			workspace_id, title, status, priority, assignee_type, assignee_id,
			creator_type, creator_id, number, position
		)
		VALUES (
			$1, 'busy acceptance retry', 'todo', 'none', 'agent', $2,
			'member', $3,
			(SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1),
			0
		)
		RETURNING id
	`, testWorkspaceID, agentID, testUserID).Scan(&issueID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(context.Background(), `
		SELECT runtime_id FROM agent WHERE id = $1
	`, agentID).Scan(&runtimeID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})
	var blockerTaskID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority, started_at
		)
		VALUES ($1, $2, $3, 'running', 0, now())
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&blockerTaskID); err != nil {
		t.Fatal(err)
	}

	endpointID, deliverySecret := createAgentDispatchEndpointForTest(t, testUserID, agentID)
	endpoint, err := testHandler.Queries.GetAgentDispatchEndpointByEndpointID(context.Background(), endpointID)
	if err != nil {
		t.Fatal(err)
	}
	const idempotencyKey = "busy-issue-acceptance-retry"
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM agent_dispatch_acceptance
			WHERE endpoint_id = $1 AND idempotency_key = $2
		`, endpoint.ID, idempotencyKey)
	})
	body := fmt.Sprintf(`{
		"schemaVersion":"2.0",
		"continuation":{"kind":"issue","issueId":%q},
		"completionCallback":{"url":"/api/v1/dispatch-tasks/test-busy-acceptance-retry/execution-result"},
		"source":{"platform":"dingtalk","type":"digital_employee"},
		"event":{
			"domain":"channel",
			"type":"message.created",
			"data":{
				"conversation":{"openConversationId":"cid-busy-acceptance-retry","type":"single"},
				"sender":{"displayName":"张三"},
				"messages":[{"openMsgId":"msg-busy-acceptance-retry","occurredAt":1784512800000,"text":"retry after busy"}]
			}
		},
		"surface":{"type":"issue"},
		"outbound":{"mode":"dws","replyTo":"latest_message"}
	}`, issueID)
	dispatch := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/webhooks/agent-dispatch", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+deliverySecret)
		req.Header.Set("Idempotency-Key", idempotencyKey)
		req = withURLParams(req, "endpointId", endpointID)
		w := httptest.NewRecorder()
		testHandler.HandleAgentDispatch(w, req)
		return w
	}

	busy := dispatch()
	if busy.Code != http.StatusConflict {
		t.Fatalf("busy dispatch: expected 409, got %d: %s", busy.Code, busy.Body.String())
	}
	var acceptanceCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM agent_dispatch_acceptance
		WHERE endpoint_id = $1 AND idempotency_key = $2
	`, endpoint.ID, idempotencyKey).Scan(&acceptanceCount); err != nil {
		t.Fatal(err)
	}
	if acceptanceCount != 0 {
		t.Fatalf("busy response retained %d acceptance rows, want 0", acceptanceCount)
	}
	if _, err := testPool.Exec(context.Background(), `
		DELETE FROM agent_task_queue WHERE id = $1
	`, blockerTaskID); err != nil {
		t.Fatal(err)
	}

	retry := dispatch()
	if retry.Code != http.StatusCreated {
		t.Fatalf("retry after busy: expected 201, got %d: %s", retry.Code, retry.Body.String())
	}
	var response AgentDispatchResponse
	if err := json.Unmarshal(retry.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.TaskID == "" || response.CommentID == "" {
		t.Fatalf("retry response missing identifiers: %+v", response)
	}
}

func TestHandleAgentDispatchV2DigitalEmployeeChatDWSIgnoresRobotInstallation(t *testing.T) {
	agentID := createHandlerTestAgent(t, "test-v2-digital-employee-chat-dws", nil)
	endpointID, deliverySecret := createAgentDispatchEndpointForTest(t, testUserID, agentID)
	const idempotencyKey = "digital-employee-chat-dws"
	installations := configureDingTalkChatDispatchForTest(t)
	robotInstallation, err := installations.Upsert(context.Background(), dingtalk.InstallationParams{
		WorkspaceID:        parseUUID(testWorkspaceID),
		AgentID:            parseUUID(agentID),
		ClientID:           "test-v2-digital-employee-stream-client",
		ClientSecret:       "test-v2-digital-employee-stream-secret",
		InstallerUserID:    parseUUID(testUserID),
		TransportMode:      dingtalk.TransportModeStream,
		RobotCode:          "test-v2-digital-employee-stream-robot",
		DispatchEndpointID: endpointID,
	})
	if err != nil {
		t.Fatalf("create unrelated Stream installation: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM channel_installation WHERE id = $1`, robotInstallation.ID)
	})

	body := fmt.Sprintf(`{
		"schemaVersion":"2.0",
		"agentId":%q,
		"continuation":null,
		"completionCallback":{"url":"/api/v1/dispatch-tasks/test-digital-chat/execution-result"},
		"source":{"platform":"dingtalk","type":"digital_employee"},
		"event":{
			"domain":"channel",
			"type":"message.created",
			"data":{
				"conversation":{"openConversationId":"cid-digital-employee","type":"group","title":"数字员工群"},
				"sender":{"displayName":"张三","openDingTalkId":"open-digital-employee-sender"},
				"messages":[{"openMsgId":"msg-digital-employee","occurredAt":1784512800000,"text":"处理数字员工消息"}]
			}
		},
		"surface":{"type":"chat"},
		"outbound":{"mode":"dws","replyTo":"latest_message"},
		"externalIdentity":{"contextToken":"sealed-digital-employee-context"}
	}`, agentID)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/agent-dispatch", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+deliverySecret)
	req.Header.Set("Idempotency-Key", idempotencyKey)
	req = withURLParams(req, "endpointId", endpointID)
	w := httptest.NewRecorder()

	testHandler.HandleAgentDispatch(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("digital employee chat+DWS: expected 202, got %d: %s", w.Code, w.Body.String())
	}
	var response AgentChatDispatchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Continuation.Kind != "chat" || response.Continuation.ChatSessionID == "" || response.TaskID == "" {
		t.Fatalf("unexpected response: %+v", response)
	}
	originalBody := append([]byte(nil), w.Body.Bytes()...)
	endpoint, err := testHandler.Queries.GetAgentDispatchEndpointByEndpointID(context.Background(), endpointID)
	if err != nil {
		t.Fatalf("load authenticated dispatch endpoint: %v", err)
	}
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_dispatch_acceptance
		SET status = 'pending',
		    response_status = NULL,
		    response_content_type = NULL,
		    response_body = NULL,
		    root_task_id = NULL,
		    lease_token = gen_random_uuid(),
		    lease_expires_at = now() - interval '1 second'
		WHERE endpoint_id = $1
	`, endpoint.ID); err != nil {
		t.Fatalf("simulate acceptance finalize failure: %v", err)
	}
	replayReq := httptest.NewRequest(http.MethodPost, "/api/webhooks/agent-dispatch", strings.NewReader(body))
	replayReq.Header.Set("Content-Type", "application/json")
	replayReq.Header.Set("Authorization", "Bearer "+deliverySecret)
	replayReq.Header.Set("Idempotency-Key", idempotencyKey)
	replayReq = withURLParams(replayReq, "endpointId", endpointID)
	replayWriter := httptest.NewRecorder()

	testHandler.HandleAgentDispatch(replayWriter, replayReq)

	if replayWriter.Code != http.StatusAccepted {
		t.Fatalf("digital employee duplicate: expected 202, got %d: %s", replayWriter.Code, replayWriter.Body.String())
	}
	if !bytes.Equal(replayWriter.Body.Bytes(), originalBody) {
		t.Fatalf("recovered response differs:\n got: %s\nwant: %s", replayWriter.Body.Bytes(), originalBody)
	}
	var replayResponse AgentChatDispatchResponse
	if err := json.Unmarshal(replayWriter.Body.Bytes(), &replayResponse); err != nil {
		t.Fatal(err)
	}
	if replayResponse.Continuation.Kind != "chat" ||
		replayResponse.Continuation.ChatSessionID != response.Continuation.ChatSessionID ||
		replayResponse.TaskID != response.TaskID {
		t.Fatalf("duplicate continuation = %+v, want original %+v", replayResponse.Continuation, response.Continuation)
	}

	refreshedIdentityBody := strings.Replace(
		body,
		"sealed-digital-employee-context",
		"refreshed-digital-employee-context",
		1,
	)
	refreshedIdentityReq := httptest.NewRequest(
		http.MethodPost,
		"/api/webhooks/agent-dispatch",
		strings.NewReader(refreshedIdentityBody),
	)
	refreshedIdentityReq.Header.Set("Content-Type", "application/json")
	refreshedIdentityReq.Header.Set("Authorization", "Bearer "+deliverySecret)
	refreshedIdentityReq.Header.Set("Idempotency-Key", idempotencyKey)
	refreshedIdentityReq = withURLParams(refreshedIdentityReq, "endpointId", endpointID)
	refreshedIdentityWriter := httptest.NewRecorder()
	testHandler.HandleAgentDispatch(refreshedIdentityWriter, refreshedIdentityReq)
	if refreshedIdentityWriter.Code != http.StatusAccepted ||
		!bytes.Equal(refreshedIdentityWriter.Body.Bytes(), originalBody) {
		t.Fatalf(
			"refreshed context token did not replay original response: status=%d body=%s",
			refreshedIdentityWriter.Code,
			refreshedIdentityWriter.Body.String(),
		)
	}

	changedCallbackBody := strings.Replace(
		body,
		"test-digital-chat",
		"test-digital-chat-changed",
		1,
	)
	changedCallbackReq := httptest.NewRequest(
		http.MethodPost,
		"/api/webhooks/agent-dispatch",
		strings.NewReader(changedCallbackBody),
	)
	changedCallbackReq.Header.Set("Content-Type", "application/json")
	changedCallbackReq.Header.Set("Authorization", "Bearer "+deliverySecret)
	changedCallbackReq.Header.Set("Idempotency-Key", idempotencyKey)
	changedCallbackReq = withURLParams(changedCallbackReq, "endpointId", endpointID)
	changedCallbackWriter := httptest.NewRecorder()
	testHandler.HandleAgentDispatch(changedCallbackWriter, changedCallbackReq)
	if changedCallbackWriter.Code != http.StatusConflict {
		t.Fatalf(
			"same idempotency key with changed callback: expected 409, got %d: %s",
			changedCallbackWriter.Code,
			changedCallbackWriter.Body.String(),
		)
	}

	differentKeyReq := httptest.NewRequest(
		http.MethodPost,
		"/api/webhooks/agent-dispatch",
		strings.NewReader(changedCallbackBody),
	)
	differentKeyReq.Header.Set("Content-Type", "application/json")
	differentKeyReq.Header.Set("Authorization", "Bearer "+deliverySecret)
	differentKeyReq.Header.Set("Idempotency-Key", idempotencyKey+"-different")
	differentKeyReq = withURLParams(differentKeyReq, "endpointId", endpointID)
	differentKeyWriter := httptest.NewRecorder()
	testHandler.HandleAgentDispatch(differentKeyWriter, differentKeyReq)
	if differentKeyWriter.Code != http.StatusConflict {
		t.Fatalf(
			"same openMsgId under a different dispatch was mis-recovered: expected 409, got %d: %s",
			differentKeyWriter.Code,
			differentKeyWriter.Body.String(),
		)
	}

	var namespaceID, workspaceID, sessionAgentID string
	if err := testPool.QueryRow(context.Background(), `
		SELECT binding.installation_id, session.workspace_id, session.agent_id
		FROM channel_chat_session_binding binding
		JOIN chat_session session ON session.id = binding.chat_session_id
		WHERE binding.chat_session_id = $1
	`, response.Continuation.ChatSessionID).Scan(&namespaceID, &workspaceID, &sessionAgentID); err != nil {
		t.Fatalf("load digital employee chat namespace: %v", err)
	}
	if namespaceID != uuidToString(endpoint.ID) {
		t.Fatalf("chat namespace = %s, want authenticated endpoint %s", namespaceID, uuidToString(endpoint.ID))
	}
	if namespaceID == uuidToString(robotInstallation.ID) {
		t.Fatalf("digital employee chat reused robot installation namespace %s", namespaceID)
	}
	if workspaceID != testWorkspaceID || sessionAgentID != agentID {
		t.Fatalf("chat scope = workspace %s agent %s, want %s/%s", workspaceID, sessionAgentID, testWorkspaceID, agentID)
	}
	var processed bool
	if err := testPool.QueryRow(context.Background(), `
		SELECT processed_at IS NOT NULL
		FROM channel_inbound_message_dedup
		WHERE installation_id = $1 AND message_id = $2
	`, endpoint.ID, "msg-digital-employee").Scan(&processed); err != nil {
		t.Fatalf("load digital employee dedup row: %v", err)
	}
	if !processed {
		t.Fatal("digital employee dedup row was not finalized")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM channel_inbound_message_dedup WHERE installation_id = $1`, endpoint.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM channel_chat_session_binding WHERE installation_id = $1`, endpoint.ID)
	})
}

func TestHandleAgentDispatchV2RecoversSynchronousCompletionAfterAcceptanceFinalizeFailure(t *testing.T) {
	agentID := createHandlerTestAgent(t, "test-v2-sync-acceptance-recovery", nil)
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent SET archived_at = now(), archived_by = $2 WHERE id = $1
	`, agentID, testUserID); err != nil {
		t.Fatal(err)
	}
	configureDingTalkChatDispatchForTest(t)
	endpointID, deliverySecret := createAgentDispatchEndpointForTest(t, testUserID, agentID)
	const idempotencyKey = "sync-acceptance-recovery"
	const dispatchTaskID = "test-sync-acceptance-recovery"
	body := fmt.Sprintf(`{
		"schemaVersion":"2.0",
		"agentId":%q,
		"continuation":null,
		"completionCallback":{"url":"/api/v1/dispatch-tasks/%s/execution-result"},
		"source":{"platform":"dingtalk","type":"digital_employee"},
		"event":{
			"domain":"channel",
			"type":"message.created",
			"data":{
				"conversation":{"openConversationId":"cid-sync-acceptance-recovery","type":"single"},
				"sender":{"displayName":"张三","openDingTalkId":"open-sync-acceptance-recovery"},
				"messages":[{"openMsgId":"msg-sync-acceptance-recovery","occurredAt":1784512800000,"text":"sync acceptance recovery"}]
			}
		},
		"surface":{"type":"chat"},
		"outbound":{"mode":"dws","replyTo":"latest_message"},
		"externalIdentity":{"contextToken":"sealed-sync-acceptance-recovery"}
	}`, agentID, dispatchTaskID)
	dispatch := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/webhooks/agent-dispatch", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+deliverySecret)
		req.Header.Set("Idempotency-Key", idempotencyKey)
		req = withURLParams(req, "endpointId", endpointID)
		w := httptest.NewRecorder()
		testHandler.HandleAgentDispatch(w, req)
		return w
	}

	first := dispatch()
	if first.Code != http.StatusAccepted || first.Body.Len() != 0 {
		t.Fatalf("initial synchronous completion: expected empty 202, got %d: %s", first.Code, first.Body.String())
	}
	endpoint, err := testHandler.Queries.GetAgentDispatchEndpointByEndpointID(context.Background(), endpointID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM agent_dispatch_acceptance
			WHERE endpoint_id = $1 AND idempotency_key = $2
		`, endpoint.ID, idempotencyKey)
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM task_completion_outbox WHERE request_id = $1
		`, "multica-terminal:sync:"+dispatchTaskID)
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM channel_inbound_message_dedup WHERE installation_id = $1
		`, endpoint.ID)
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM channel_chat_session_binding WHERE installation_id = $1
		`, endpoint.ID)
	})
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_dispatch_acceptance
		SET status = 'pending',
		    response_status = NULL,
		    response_content_type = NULL,
		    response_body = NULL,
		    root_task_id = NULL,
		    lease_token = gen_random_uuid(),
		    lease_expires_at = now() - interval '1 second'
		WHERE endpoint_id = $1 AND idempotency_key = $2
	`, endpoint.ID, idempotencyKey); err != nil {
		t.Fatalf("simulate synchronous acceptance finalize failure: %v", err)
	}

	replay := dispatch()
	if replay.Code != http.StatusAccepted || replay.Body.Len() != 0 {
		t.Fatalf("recovered synchronous completion: expected empty 202, got %d: %s", replay.Code, replay.Body.String())
	}
	var completionCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM task_completion_outbox WHERE request_id = $1
	`, "multica-terminal:sync:"+dispatchTaskID).Scan(&completionCount); err != nil {
		t.Fatal(err)
	}
	if completionCount != 1 {
		t.Fatalf("synchronous completion count = %d, want 1", completionCount)
	}
}

func configureDingTalkChatDispatchForTest(t *testing.T) *dingtalk.InstallationService {
	t.Helper()
	box, err := secretbox.New(make([]byte, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	installations, err := dingtalk.NewInstallationService(testHandler.Queries, testPool, box)
	if err != nil {
		t.Fatal(err)
	}
	router := engine.NewRouter(
		testHandler.IssueService,
		testHandler.TaskService,
		testHandler.Queries,
		engine.RouterConfig{},
	)
	router.Register(dingtalk.TypeDingtalk, dingtalk.NewDingTalkResolverSet(
		testHandler.Queries,
		testPool,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	))
	previousRouter := testHandler.ChannelRouter
	previousInstallations := testHandler.DingTalkInstallations
	testHandler.ChannelRouter = router
	testHandler.DingTalkInstallations = installations
	t.Cleanup(func() {
		testHandler.ChannelRouter = previousRouter
		testHandler.DingTalkInstallations = previousInstallations
	})
	return installations
}

func TestHandleAgentDispatchV2RobotSDKCompletionDependsOnlyOnCallbackPresence(t *testing.T) {
	for _, tc := range []struct {
		name         string
		withCallback bool
	}{
		{name: "callback persists acceptance and terminal outbox", withCallback: true},
		{name: "callback absent keeps legacy no-callback path", withCallback: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agentID := createHandlerTestAgent(
				t,
				"test-v2-robot-callback-"+strings.ReplaceAll(tc.name, " ", "-"),
				nil,
			)
			endpointID, deliverySecret := createAgentDispatchEndpointForTest(t, testUserID, agentID)
			installations := configureDingTalkChatDispatchForTest(t)
			robotInstallation, err := installations.Upsert(context.Background(), dingtalk.InstallationParams{
				WorkspaceID:        parseUUID(testWorkspaceID),
				AgentID:            parseUUID(agentID),
				ClientID:           "client-robot-callback-" + strings.ReplaceAll(tc.name, " ", "-"),
				ClientSecret:       "secret-robot-callback-" + tc.name,
				InstallerUserID:    parseUUID(testUserID),
				TransportMode:      dingtalk.TransportModeHTTPCallback,
				RobotCode:          "robot-callback-" + strings.ReplaceAll(tc.name, " ", "-"),
				DispatchEndpointID: endpointID,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = testPool.Exec(context.Background(), `
					DELETE FROM channel_installation WHERE id = $1
				`, robotInstallation.ID)
			})

			suffix := "robot-callback-" + strings.ReplaceAll(tc.name, " ", "-")
			callbackJSON := ""
			if tc.withCallback {
				callbackJSON = fmt.Sprintf(
					`, "completionCallback":{"url":"/api/v1/dispatch-tasks/%s/execution-result"}`,
					suffix,
				)
			}
			body := fmt.Sprintf(`{
				"schemaVersion":"2.0",
				"agentId":%q,
				"continuation":null%s,
				"source":{"platform":"dingtalk","type":"robot"},
				"event":{
					"domain":"channel",
					"type":"message.created",
					"data":{
						"conversation":{"openConversationId":"cid-%s","type":"group","title":"机器人群"},
						"sender":{"displayName":"张三","openDingTalkId":"open-robot-sender"},
						"messages":[{"openMsgId":"msg-%s","occurredAt":1784512800000,"text":"处理机器人消息"}]
					}
				},
				"surface":{"type":"chat"},
				"outbound":{"mode":"robot_sdk","replyTo":"latest_message"},
				"externalIdentity":{"contextToken":"sealed-robot-context"}
			}`, agentID, callbackJSON, suffix, suffix)
			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/agent-dispatch", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+deliverySecret)
			req.Header.Set("Idempotency-Key", suffix)
			req = withURLParams(req, "endpointId", endpointID)
			w := httptest.NewRecorder()

			testHandler.HandleAgentDispatch(w, req)

			if w.Code != http.StatusAccepted {
				t.Fatalf("robot callback dispatch: expected 202, got %d: %s", w.Code, w.Body.String())
			}
			var response AgentChatDispatchResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.TaskID == "" || response.Continuation.ChatSessionID == "" {
				t.Fatalf("robot callback dispatch response = %+v", response)
			}
			t.Cleanup(func() {
				_, _ = testPool.Exec(context.Background(), `
					DELETE FROM task_completion_outbox WHERE root_task_id = $1
				`, response.TaskID)
				_, _ = testPool.Exec(context.Background(), `
					DELETE FROM channel_inbound_message_dedup WHERE installation_id = $1
				`, robotInstallation.ID)
				_, _ = testPool.Exec(context.Background(), `
					DELETE FROM channel_chat_session_binding WHERE installation_id = $1
				`, robotInstallation.ID)
			})

			var callbackURL, callbackTarget string
			if err := testPool.QueryRow(context.Background(), `
				SELECT
					COALESCE(context #>> '{completion_callback,url}', ''),
					COALESCE(context #>> '{completion_callback,target}', '')
				FROM agent_task_queue
				WHERE id = $1
			`, response.TaskID).Scan(&callbackURL, &callbackTarget); err != nil {
				t.Fatal(err)
			}
			if tc.withCallback {
				wantURL := "/api/v1/dispatch-tasks/" + suffix + "/execution-result"
				if callbackURL != wantURL || callbackTarget != testRouterTargetIdentity {
					t.Fatalf("persisted callback = %q/%q, want %q/%q",
						callbackURL, callbackTarget, wantURL, testRouterTargetIdentity)
				}
				var acceptanceStatus, acceptanceTarget, acceptedRootTaskID string
				if err := testPool.QueryRow(context.Background(), `
					SELECT acceptance.status, acceptance.target_identity, acceptance.root_task_id
					FROM agent_dispatch_acceptance acceptance
					JOIN agent_dispatch_endpoint endpoint ON endpoint.id = acceptance.endpoint_id
					WHERE endpoint.endpoint_id = $1 AND acceptance.idempotency_key = $2
				`, endpointID, suffix).Scan(
					&acceptanceStatus,
					&acceptanceTarget,
					&acceptedRootTaskID,
				); err != nil {
					t.Fatal(err)
				}
				if acceptanceStatus != "accepted" ||
					acceptanceTarget != testRouterTargetIdentity ||
					acceptedRootTaskID != response.TaskID {
					t.Fatalf("acceptance = status:%q target:%q root:%q",
						acceptanceStatus, acceptanceTarget, acceptedRootTaskID)
				}
			} else {
				if callbackURL != "" || callbackTarget != "" {
					t.Fatalf("callback-absent task persisted completion callback %q/%q",
						callbackURL, callbackTarget)
				}
				var acceptanceCount int
				if err := testPool.QueryRow(context.Background(), `
					SELECT count(*)
					FROM agent_dispatch_acceptance acceptance
					JOIN agent_dispatch_endpoint endpoint ON endpoint.id = acceptance.endpoint_id
					WHERE endpoint.endpoint_id = $1 AND acceptance.idempotency_key = $2
				`, endpointID, suffix).Scan(&acceptanceCount); err != nil {
					t.Fatal(err)
				}
				if acceptanceCount != 0 {
					t.Fatalf("callback-absent dispatch created %d acceptance rows", acceptanceCount)
				}
			}

			if _, err := testPool.Exec(context.Background(), `
				UPDATE agent_task_queue
				SET status = 'running', started_at = now(), dispatched_at = now()
				WHERE id = $1
			`, response.TaskID); err != nil {
				t.Fatal(err)
			}
			if _, err := testHandler.TaskService.CompleteTask(
				context.Background(),
				parseUUID(response.TaskID),
				completeResult(t, "robot final reply"),
				"",
				"",
			); err != nil {
				t.Fatal(err)
			}

			var completionCount int
			if err := testPool.QueryRow(context.Background(), `
				SELECT count(*) FROM task_completion_outbox WHERE root_task_id = $1
			`, response.TaskID).Scan(&completionCount); err != nil {
				t.Fatal(err)
			}
			if !tc.withCallback {
				if completionCount != 0 {
					t.Fatalf("callback-absent terminal task created %d outbox rows", completionCount)
				}
				return
			}
			if completionCount != 1 {
				t.Fatalf("robot callback terminal task created %d outbox rows", completionCount)
			}
			var executionStatus, resultMessage string
			if err := testPool.QueryRow(context.Background(), `
				SELECT execution_status, result_message
				FROM task_completion_outbox
				WHERE root_task_id = $1
			`, response.TaskID).Scan(&executionStatus, &resultMessage); err != nil {
				t.Fatal(err)
			}
			if executionStatus != "completed" || resultMessage != "robot final reply" {
				t.Fatalf("robot terminal completion = status:%q result:%q",
					executionStatus, resultMessage)
			}
		})
	}
}

func TestHandleAgentDispatchV2RobotSDKChatKeepsInstallationGuards(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		sourceType                  string
		transportMode               dingtalk.TransportMode
		installationEndpointMatches bool
		withCallback               bool
		wantStatus                  int
	}{
		{name: "robot callback rejects Stream installation", sourceType: "robot", transportMode: dingtalk.TransportModeStream, installationEndpointMatches: true, withCallback: true, wantStatus: http.StatusForbidden},
		{name: "robot rejects different dispatch endpoint", sourceType: "robot", transportMode: dingtalk.TransportModeHTTPCallback, wantStatus: http.StatusForbidden},
		{name: "digital employee robot SDK callback rejects Stream installation", sourceType: "digital_employee", transportMode: dingtalk.TransportModeStream, installationEndpointMatches: true, withCallback: true, wantStatus: http.StatusForbidden},
		{name: "robot accepts matching HTTP callback installation", sourceType: "robot", transportMode: dingtalk.TransportModeHTTPCallback, installationEndpointMatches: true, wantStatus: http.StatusAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agentID := createHandlerTestAgent(t, "test-v2-robot-sdk-"+strings.ReplaceAll(tc.name, " ", "-"), nil)
			endpointID, deliverySecret := createAgentDispatchEndpointForTest(t, testUserID, agentID)
			installations := configureDingTalkChatDispatchForTest(t)
			installationEndpointID := "v1_different_dispatch_endpoint"
			if tc.installationEndpointMatches {
				installationEndpointID = endpointID
			}
			robotInstallation, err := installations.Upsert(context.Background(), dingtalk.InstallationParams{
				WorkspaceID:        parseUUID(testWorkspaceID),
				AgentID:            parseUUID(agentID),
				ClientID:           "client-" + strings.ReplaceAll(tc.name, " ", "-"),
				ClientSecret:       "secret-" + tc.name,
				InstallerUserID:    parseUUID(testUserID),
				TransportMode:      tc.transportMode,
				RobotCode:          "robot-" + strings.ReplaceAll(tc.name, " ", "-"),
				DispatchEndpointID: installationEndpointID,
			})
			if err != nil {
				t.Fatalf("create robot installation: %v", err)
			}
			t.Cleanup(func() {
				_, _ = testPool.Exec(context.Background(), `DELETE FROM channel_installation WHERE id = $1`, robotInstallation.ID)
			})

			messageID := "msg-" + strings.ReplaceAll(tc.name, " ", "-")
			completionCallback := ""
			if tc.withCallback {
				completionCallback = `,
				"completionCallback":{"url":"/api/v1/dispatch-tasks/test-robot-sdk-installation-guard/execution-result"}`
			}
			body := fmt.Sprintf(`{
				"schemaVersion":"2.0",
				"agentId":%q,
				"continuation":null%s,
				"source":{"platform":"dingtalk","type":%q},
				"event":{
					"domain":"channel",
					"type":"message.created",
					"data":{
						"conversation":{"openConversationId":"cid-robot","type":"group","title":"机器人群"},
						"sender":{"displayName":"张三","openDingTalkId":"open-robot-sender"},
						"messages":[{"openMsgId":%q,"occurredAt":1784512800000,"text":"处理机器人消息"}]
					}
				},
				"surface":{"type":"chat"},
				"outbound":{"mode":"robot_sdk","replyTo":"latest_message"},
				"externalIdentity":{"contextToken":"sealed-robot-context"}
			}`, agentID, completionCallback, tc.sourceType, messageID)
			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/agent-dispatch", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+deliverySecret)
			req = withURLParams(req, "endpointId", endpointID)
			w := httptest.NewRecorder()

			testHandler.HandleAgentDispatch(w, req)

			if w.Code != tc.wantStatus {
				t.Fatalf("robot SDK chat: expected %d, got %d: %s", tc.wantStatus, w.Code, w.Body.String())
			}
			if tc.wantStatus == http.StatusForbidden {
				if !strings.Contains(w.Body.String(), "dingtalk robot installation is not an HTTP callback source") {
					t.Fatalf("unexpected robot installation rejection: %s", w.Body.String())
				}
				return
			}

			var response AgentChatDispatchResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Continuation.Kind != "chat" || response.Continuation.ChatSessionID == "" || response.TaskID == "" {
				t.Fatalf("unexpected robot chat response: %+v", response)
			}
			var namespaceID string
			if err := testPool.QueryRow(context.Background(), `
				SELECT installation_id
				FROM channel_chat_session_binding
				WHERE chat_session_id = $1
			`, response.Continuation.ChatSessionID).Scan(&namespaceID); err != nil {
				t.Fatalf("load robot chat namespace: %v", err)
			}
			if namespaceID != uuidToString(robotInstallation.ID) {
				t.Fatalf("robot chat namespace = %s, want installation %s", namespaceID, uuidToString(robotInstallation.ID))
			}
			t.Cleanup(func() {
				_, _ = testPool.Exec(context.Background(), `DELETE FROM channel_inbound_message_dedup WHERE installation_id = $1`, robotInstallation.ID)
				_, _ = testPool.Exec(context.Background(), `DELETE FROM channel_chat_session_binding WHERE installation_id = $1`, robotInstallation.ID)
			})
		})
	}
}
