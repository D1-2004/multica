package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func createDelegationSourceTask(t *testing.T, agentID, contextJSON string) (taskID, chatSessionID string) {
	t.Helper()
	chatSessionID = createHandlerTestChatSession(t, agentID)
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, issue_id, status, priority, chat_session_id,
			initiator_user_id, originator_user_id, accountable_user_id, context, started_at
		)
		VALUES ($1, $2, NULL, 'running', 2, $3, $4, $4, $4, $5::jsonb, now())
		RETURNING id
	`, agentID, testRuntimeID, chatSessionID, testUserID, contextJSON).Scan(&taskID); err != nil {
		t.Fatalf("create source task: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM task_execution_update_outbox WHERE root_task_id = $1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE root_task_id = $1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})
	return taskID, chatSessionID
}

func delegationRequest(t *testing.T, sourceTaskID, sourceAgentID string, body map[string]any) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/issue-delegations", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Task-ID", sourceTaskID)
	req.Header.Set("X-Agent-ID", sourceAgentID)
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	return req
}

func TestDelegatedIssueDispatchPreservesExternalDWSAndContextPrompt(t *testing.T) {
	source := db.AgentTaskQueue{Context: []byte(`{
		"dispatch_schema_version":"2.0",
		"dispatch_source":{"platform":"dingtalk","type":"digital_employee"},
		"dispatch_domain":"channel",
		"dispatch_type":"message.created",
		"dispatch_event_data":{"conversation":{"openConversationId":"cid-delegation"},"messages":[{"openMsgId":"msg-delegation","text":"继续处理"}]},
		"dispatch_outbound":{"mode":"dws","replyTo":"latest_message"},
		"dispatch_context_prompt":"ROUTER CONTEXT",
		"agent_identity_context_token":"private-context-token",
		"agent_identity_context_token_expires_at":4102444800,
		"external_identity":{"dws":{"uid":"24710833","orgId":"439446171"}}
	}`)}

	command, _, transferred, err := delegatedIssueDispatch(source)
	if err != nil {
		t.Fatalf("delegatedIssueDispatch: %v", err)
	}
	if command.ContextPrompt != "ROUTER CONTEXT" {
		t.Fatalf("delegated context prompt = %q", command.ContextPrompt)
	}
	if command.ExternalIdentity.DWS == nil ||
		command.ExternalIdentity.DWS.UID != "24710833" ||
		command.ExternalIdentity.DWS.OrgID != "439446171" {
		t.Fatalf("delegated external DWS identity = %#v", command.ExternalIdentity.DWS)
	}
	if command.ExternalIdentity.ContextToken != "private-context-token" ||
		command.ExternalIdentity.ExpiresAt != 4102444800 {
		t.Fatalf("delegated context token identity = %#v", command.ExternalIdentity)
	}
	for _, inherited := range []string{"dispatch_context_prompt", "ROUTER CONTEXT", "external_identity", "24710833", "439446171"} {
		if !strings.Contains(string(transferred), inherited) {
			t.Errorf("transferred context missing %q: %s", inherited, transferred)
		}
	}
}

func TestDelegateIssueCreateTransfersPrivateContextAndCompletionResponsibility(t *testing.T) {
	sourceAgentID := createHandlerTestAgent(t, "delegation-source-agent", nil)
	targetAgentID := createHandlerTestAgent(t, "delegation-target-agent", nil)
	callbackURL := "/api/v1/dispatch-tasks/router-task-123/execution-result"
	updateURL := "/api/v1/dispatch-tasks/router-task-123/execution-update"
	sourceContext := fmt.Sprintf(`{
		"dispatch_schema_version":"2.0",
		"dispatch_source":{"platform":"dingtalk","type":"digital_employee"},
		"dispatch_domain":"channel",
		"dispatch_type":"message.created",
		"dispatch_event_data":{
			"conversation":{"openConversationId":"cid-delegation","type":"group"},
			"sender":{"openDingTalkId":"sender-delegation","displayName":"测试用户"},
			"messages":[{"openMsgId":"msg-delegation","occurredAt":1,"text":"搜索今天的科技新闻"}]
		},
		"dispatch_surface":{"type":"chat"},
		"dispatch_outbound":{"mode":"dws","replyTo":"latest_message"},
		"dispatch_context_prompt":"ROUTER CONTEXT",
		"dispatch_idempotency_key":"dispatch-window:delegation",
		"parent_ref":{"type":"router_task","id":"router-task-123"},
		"unknown_large_number":9007199254740993,
		"agent_identity_context_token":"private-context-token",
		"agent_identity_context_token_expires_at":4102444800,
		"external_identity":{"dws":{"uid":"24710833","orgId":"439446171"}},
		"completion_callback":{"url":%q,"update_url":%q,"target":%q}
	}`, callbackURL, updateURL, testRouterTargetIdentity)
	sourceTaskID, chatSessionID := createDelegationSourceTask(t, sourceAgentID, sourceContext)

	delegationBody := map[string]any{
		"source_task_id": sourceTaskID,
		"mode":           "create",
		"title":          "采集 2026-07-29 科技新闻并生成消息卡片",
		"description":    "采集公开科技新闻，整理成消息卡片并发送。",
		"assignee_id":    targetAgentID,
	}
	req := delegationRequest(t, sourceTaskID, sourceAgentID, delegationBody)
	w := httptest.NewRecorder()
	testHandler.DelegateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("DelegateIssue = %d: %s", w.Code, w.Body.String())
	}

	var response struct {
		IssueID      string `json:"issue_id"`
		TargetTaskID string `json:"target_task_id"`
		Release      bool   `json:"release_parent"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.IssueID == "" || response.TargetTaskID == "" {
		t.Fatalf("incomplete response: %+v", response)
	}
	if !response.Release {
		t.Fatalf("response = %+v", response)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, response.IssueID)
	})

	var metadata []byte
	if err := testPool.QueryRow(context.Background(), `
		SELECT metadata FROM issue WHERE id = $1
	`, response.IssueID).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(metadata), chatSessionID) || !strings.Contains(string(metadata), sourceTaskID) {
		t.Fatalf("issue metadata = %s", metadata)
	}
	var labelName, labelDescription string
	if err := testPool.QueryRow(context.Background(), `
		SELECT label.name, label.description
		FROM issue_label label
		JOIN issue_to_label link ON link.label_id = label.id
		WHERE link.issue_id = $1
	`, response.IssueID).Scan(&labelName, &labelDescription); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(labelName, "chat:") || len(labelName) > 32 || !strings.Contains(labelDescription, chatSessionID) {
		t.Fatalf("label = %q description=%q", labelName, labelDescription)
	}

	targetTaskUUID, err := util.ParseUUID(response.TargetTaskID)
	if err != nil {
		t.Fatal(err)
	}
	targetTask, err := testHandler.Queries.GetAgentTask(context.Background(), targetTaskUUID)
	if err != nil {
		t.Fatal(err)
	}
	for _, inherited := range []string{"private-context-token", "dispatch_schema_version", "dispatch_context_prompt", "ROUTER CONTEXT", "parent_ref", "router-task-123", "completion_callback", "9007199254740993", "external_identity", "24710833", "439446171"} {
		if !strings.Contains(string(targetTask.Context), inherited) {
			t.Errorf("target context missing %q: %s", inherited, targetTask.Context)
		}
	}
	if uuidToString(targetTask.ParentTaskID) != sourceTaskID {
		t.Fatalf("target parent task = %s, want %s", uuidToString(targetTask.ParentTaskID), sourceTaskID)
	}
	var handoffRootTaskID, handoffTargetTaskID, handoffIssueID string
	var handoffCallbackURL, handoffTargetIdentity, handoffRequestID string
	var handoffAgentID, handoffTargetAgentID, handoffUpdateType, handoffStatus string
	if err := testPool.QueryRow(context.Background(), `
		SELECT root_task_id::text, target_task_id::text, issue_id::text,
		       callback_url, target_identity, request_id, agent_id::text,
		       target_agent_id::text, update_type, status
		FROM task_execution_update_outbox
		WHERE root_task_id = $1
	`, sourceTaskID).Scan(
		&handoffRootTaskID,
		&handoffTargetTaskID,
		&handoffIssueID,
		&handoffCallbackURL,
		&handoffTargetIdentity,
		&handoffRequestID,
		&handoffAgentID,
		&handoffTargetAgentID,
		&handoffUpdateType,
		&handoffStatus,
	); err != nil {
		t.Fatal(err)
	}
	if handoffRootTaskID != sourceTaskID ||
		handoffTargetTaskID != response.TargetTaskID ||
		handoffIssueID != response.IssueID ||
		handoffCallbackURL != updateURL ||
		handoffTargetIdentity != testRouterTargetIdentity ||
		handoffRequestID != "multica-handoff:"+sourceTaskID ||
		handoffAgentID != sourceAgentID ||
		handoffTargetAgentID != targetAgentID ||
		handoffUpdateType != "delegated_to_issue" ||
		handoffStatus != "waiting_result" {
		t.Fatalf(
			"handoff outbox root=%q target=%q issue=%q callback=%q identity=%q request=%q agent=%q target_agent=%q type=%q status=%q",
			handoffRootTaskID,
			handoffTargetTaskID,
			handoffIssueID,
			handoffCallbackURL,
			handoffTargetIdentity,
			handoffRequestID,
			handoffAgentID,
			handoffTargetAgentID,
			handoffUpdateType,
			handoffStatus,
		)
	}
	var initialResultMessage *string
	var initialResultMessageFrozen bool
	if err := testPool.QueryRow(context.Background(), `
		SELECT result_message, result_message_frozen
		FROM task_execution_update_outbox
		WHERE root_task_id = $1
	`, sourceTaskID).Scan(&initialResultMessage, &initialResultMessageFrozen); err != nil {
		t.Fatal(err)
	}
	if initialResultMessage != nil || initialResultMessageFrozen {
		t.Fatalf("initial handoff result message = %#v frozen=%v", initialResultMessage, initialResultMessageFrozen)
	}
	var oldWorkerClaimable int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM task_execution_update_outbox
		WHERE root_task_id = $1
		  AND status = 'queued'
		  AND available_at <= now()
		  AND (lease_expires_at IS NULL OR lease_expires_at <= now())
	`, sourceTaskID).Scan(&oldWorkerClaimable); err != nil {
		t.Fatal(err)
	}
	if oldWorkerClaimable != 0 {
		t.Fatalf("old worker can claim waiting handoff: count=%d", oldWorkerClaimable)
	}
	var targetContext struct {
		Surface struct {
			Type string `json:"type"`
		} `json:"dispatch_surface"`
		DelegatedFromTaskID string `json:"dispatch_delegated_from_task_id"`
	}
	if err := json.Unmarshal(targetTask.Context, &targetContext); err != nil {
		t.Fatal(err)
	}
	if targetContext.Surface.Type != "issue" {
		t.Fatalf("target dispatch surface = %q, want issue", targetContext.Surface.Type)
	}
	if targetContext.DelegatedFromTaskID != sourceTaskID {
		t.Fatalf("delegated source marker = %q, want %q", targetContext.DelegatedFromTaskID, sourceTaskID)
	}

	sourceTaskUUID, _ := util.ParseUUID(sourceTaskID)
	if _, err := testHandler.TaskService.CompleteTask(
		context.Background(),
		sourceTaskUUID,
		[]byte(`{"output":"已转入后台处理","result_message":"任务已转入后台"}`),
		"chat-session-runtime",
		"",
		false,
		"",
	); err != nil {
		t.Fatal(err)
	}
	var frozenResultMessage *string
	var frozenResultMessageReady bool
	var frozenStatus string
	if err := testPool.QueryRow(context.Background(), `
		SELECT result_message, result_message_frozen, status
		FROM task_execution_update_outbox
		WHERE root_task_id = $1
	`, sourceTaskID).Scan(&frozenResultMessage, &frozenResultMessageReady, &frozenStatus); err != nil {
		t.Fatal(err)
	}
	if frozenResultMessage == nil || *frozenResultMessage != "任务已转入后台" ||
		!frozenResultMessageReady || frozenStatus != "queued" {
		t.Fatalf("frozen handoff result message = %#v frozen=%v status=%q", frozenResultMessage, frozenResultMessageReady, frozenStatus)
	}
	var sourceOutboxCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM task_completion_outbox WHERE root_task_id = $1
	`, sourceTaskID).Scan(&sourceOutboxCount); err != nil {
		t.Fatal(err)
	}
	if sourceOutboxCount != 0 {
		t.Fatalf("source task queued callback before background completion")
	}

	retry := httptest.NewRecorder()
	testHandler.DelegateIssue(
		retry,
		delegationRequest(t, sourceTaskID, sourceAgentID, delegationBody),
	)
	if retry.Code != http.StatusOK {
		t.Fatalf("idempotent retry after source release = %d: %s", retry.Code, retry.Body.String())
	}
	var retryResponse IssueDelegationResponse
	if err := json.Unmarshal(retry.Body.Bytes(), &retryResponse); err != nil {
		t.Fatal(err)
	}
	if retryResponse.IssueID != response.IssueID ||
		retryResponse.TargetTaskID != response.TargetTaskID {
		t.Fatalf("idempotent retry changed delegation: first=%+v retry=%+v", response, retryResponse)
	}

	if _, err := testPool.Exec(context.Background(), `
		DELETE FROM chat_session WHERE id = $1
	`, chatSessionID); err != nil {
		t.Fatal(err)
	}

	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1
	`, response.TargetTaskID); err != nil {
		t.Fatal(err)
	}
	completed, err := testHandler.TaskService.CompleteTask(
		context.Background(),
		targetTaskUUID,
		[]byte(`{"output":"科技新闻卡片已发送"}`),
		"issue-session-runtime",
		"",
		false,
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != "completed" {
		t.Fatalf("target status = %s", completed.Status)
	}

	var outbox db.TaskCompletionOutbox
	if err := testPool.QueryRow(context.Background(), `
		SELECT id, root_task_id, terminal_task_id, callback_url, target_identity,
		       request_id, agent_id, external_session_id, execution_status,
		       result_message, error, failure_reason, status, available_at,
		       attempt_count, lease_token, lease_expires_at, last_error,
		       delivered_at, created_at, updated_at
		FROM task_completion_outbox
		WHERE root_task_id = $1
	`, sourceTaskID).Scan(
		&outbox.ID, &outbox.RootTaskID, &outbox.TerminalTaskID, &outbox.CallbackUrl,
		&outbox.TargetIdentity, &outbox.RequestID, &outbox.AgentID,
		&outbox.ExternalSessionID, &outbox.ExecutionStatus, &outbox.ResultMessage,
		&outbox.Error, &outbox.FailureReason, &outbox.Status, &outbox.AvailableAt,
		&outbox.AttemptCount, &outbox.LeaseToken, &outbox.LeaseExpiresAt,
		&outbox.LastError, &outbox.DeliveredAt, &outbox.CreatedAt, &outbox.UpdatedAt,
	); err != nil {
		t.Fatal(err)
	}
	if util.UUIDToString(outbox.TerminalTaskID) != response.TargetTaskID {
		t.Fatalf("terminal task = %s", util.UUIDToString(outbox.TerminalTaskID))
	}
	if outbox.CallbackUrl != callbackURL || outbox.ResultMessage != "科技新闻卡片已发送" {
		t.Fatalf("outbox = %+v", outbox)
	}
	if util.UUIDToString(outbox.AgentID) != sourceAgentID {
		t.Fatalf("callback agent = %s, want source agent %s", util.UUIDToString(outbox.AgentID), sourceAgentID)
	}

	if _, err := testHandler.TaskService.ReconcileTaskCompletions(
		context.Background(),
		testRouterTargetIdentity,
		100,
	); err != nil {
		t.Fatal(err)
	}
	var wrongRootCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM task_completion_outbox WHERE root_task_id = $1
	`, response.TargetTaskID).Scan(&wrongRootCount); err != nil {
		t.Fatal(err)
	}
	if wrongRootCount != 0 {
		t.Fatalf("completion reconciliation created %d callbacks rooted at delegated target task", wrongRootCount)
	}
}

func TestDelegateIssueCreateRollsBackIssueWhenTaskEnqueueFails(t *testing.T) {
	sourceAgentID := createHandlerTestAgent(t, "delegation-rollback-source", nil)
	targetAgentID := createHandlerTestAgent(t, "delegation-rollback-target", nil)
	callbackURL := "/api/v1/dispatch-tasks/router-rollback/execution-result"
	sourceContext := fmt.Sprintf(`{
		"completion_callback":{"url":%q,"update_url":%q,"target":%q}
	}`,
		callbackURL,
		"/api/v1/dispatch-tasks/router-rollback/execution-update",
		testRouterTargetIdentity,
	)
	sourceTaskID, _ := createDelegationSourceTask(t, sourceAgentID, sourceContext)
	functionName := "test_fail_delegation_enqueue"
	triggerName := "test_fail_delegation_enqueue"
	if _, err := testPool.Exec(context.Background(), fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION %s()
		RETURNS trigger AS $$
		BEGIN
			RAISE EXCEPTION 'forced delegated task enqueue failure';
		END;
		$$ LANGUAGE plpgsql;
		DROP TRIGGER IF EXISTS %s ON agent_task_queue;
		CREATE TRIGGER %s
		BEFORE INSERT ON agent_task_queue
		FOR EACH ROW
		WHEN (NEW.parent_task_id = '%s'::uuid)
		EXECUTE FUNCTION %s()
	`, functionName, triggerName, triggerName, sourceTaskID, functionName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`
			DROP TRIGGER IF EXISTS %s ON agent_task_queue;
			DROP FUNCTION IF EXISTS %s()
		`, triggerName, functionName))
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM issue
			WHERE workspace_id = $1 AND title = '原子委派失败不得留下 Issue'
		`, testWorkspaceID)
	})

	w := httptest.NewRecorder()
	testHandler.DelegateIssue(w, delegationRequest(t, sourceTaskID, sourceAgentID, map[string]any{
		"source_task_id": sourceTaskID,
		"mode":           "create",
		"title":          "原子委派失败不得留下 Issue",
		"description":    "强制目标 task 创建失败。",
		"assignee_id":    targetAgentID,
	}))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("DelegateIssue = %d: %s", w.Code, w.Body.String())
	}
	var issueCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM issue
		WHERE workspace_id = $1 AND title = '原子委派失败不得留下 Issue'
	`, testWorkspaceID).Scan(&issueCount); err != nil {
		t.Fatal(err)
	}
	if issueCount != 0 {
		t.Fatalf("delegation task enqueue failure left %d issue rows", issueCount)
	}
	sourceTaskUUID, err := util.ParseUUID(sourceTaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testHandler.TaskService.CompleteTask(
		context.Background(),
		sourceTaskUUID,
		[]byte(`{"output":"后台转移失败，前台已完成闭环"}`),
		"chat-session-runtime",
		"",
		false,
		"",
	); err != nil {
		t.Fatal(err)
	}
	var queuedCallbackURL, resultMessage string
	if err := testPool.QueryRow(context.Background(), `
		SELECT callback_url, result_message
		FROM task_completion_outbox
		WHERE root_task_id = $1
	`, sourceTaskID).Scan(&queuedCallbackURL, &resultMessage); err != nil {
		t.Fatal(err)
	}
	if queuedCallbackURL != callbackURL || resultMessage != "后台转移失败，前台已完成闭环" {
		t.Fatalf("source completion callback url=%q result=%q", queuedCallbackURL, resultMessage)
	}
}

func TestDelegateIssueRejectsMemberTokenAndMismatchedTask(t *testing.T) {
	sourceAgentID := createHandlerTestAgent(t, "delegation-auth-source", nil)
	sourceTaskID, _ := createDelegationSourceTask(t, sourceAgentID, `{}`)

	base := map[string]any{
		"source_task_id": sourceTaskID,
		"mode":           "continue",
		"issue_id":       "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"content":        "继续",
	}
	t.Run("member token", func(t *testing.T) {
		req := delegationRequest(t, sourceTaskID, sourceAgentID, base)
		req.Header.Del("X-Actor-Source")
		w := httptest.NewRecorder()
		testHandler.DelegateIssue(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
	})
	t.Run("mismatched task", func(t *testing.T) {
		body := map[string]any{}
		for k, v := range base {
			body[k] = v
		}
		body["source_task_id"] = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
		req := delegationRequest(t, sourceTaskID, sourceAgentID, body)
		w := httptest.NewRecorder()
		testHandler.DelegateIssue(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d: %s", w.Code, w.Body.String())
		}
	})
}

func TestDelegateIssueWithExternalCompletionRequiresUpdateCallback(t *testing.T) {
	sourceAgentID := createHandlerTestAgent(t, "delegation-update-callback-source", nil)
	targetAgentID := createHandlerTestAgent(t, "delegation-update-callback-target", nil)
	sourceContext := fmt.Sprintf(`{
		"completion_callback":{"url":%q,"target":%q}
	}`, "/api/v1/dispatch-tasks/router-missing-update/execution-result", testRouterTargetIdentity)
	sourceTaskID, _ := createDelegationSourceTask(t, sourceAgentID, sourceContext)
	title := "缺少 handoff callback 不得释放 Chat"
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM issue WHERE workspace_id = $1 AND title = $2
		`, testWorkspaceID, title)
	})

	w := httptest.NewRecorder()
	testHandler.DelegateIssue(w, delegationRequest(t, sourceTaskID, sourceAgentID, map[string]any{
		"source_task_id": sourceTaskID,
		"mode":           "create",
		"title":          title,
		"description":    "没有非终态回调地址时不能转移控制权。",
		"assignee_id":    targetAgentID,
	}))
	if w.Code != http.StatusConflict {
		t.Fatalf("DelegateIssue = %d: %s", w.Code, w.Body.String())
	}
	var issueCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM issue WHERE workspace_id = $1 AND title = $2
	`, testWorkspaceID, title).Scan(&issueCount); err != nil {
		t.Fatal(err)
	}
	if issueCount != 0 {
		t.Fatalf("missing update callback left %d issues", issueCount)
	}
}

func TestDelegateIssueContinueCoalescesCallbacksOntoOneIssueTask(t *testing.T) {
	targetAgentID := createHandlerTestAgent(t, "delegation-queue-target", nil)
	targetAgentUUID, _ := util.ParseUUID(targetAgentID)
	workspaceUUID, _ := util.ParseUUID(testWorkspaceID)
	userUUID, _ := util.ParseUUID(testUserID)
	created, err := testHandler.IssueService.Create(context.Background(), service.IssueCreateParams{
		WorkspaceID:  workspaceUUID,
		Title:        "科技新闻日报",
		Description:  pgtype.Text{String: "采集并整理科技新闻", Valid: true},
		Status:       "todo",
		Priority:     "medium",
		AssigneeType: pgtype.Text{String: "agent", Valid: true},
		AssigneeID:   targetAgentUUID,
		CreatorType:  "member",
		CreatorID:    userUUID,
	}, service.IssueCreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	issue := created.Issue
	if created.EnqueuedTask == nil {
		t.Fatal("initial issue task was not enqueued")
	}
	initialTask := *created.EnqueuedTask
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issue.ID)
	})
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1
	`, initialTask.ID); err != nil {
		t.Fatal(err)
	}

	sourceAgentID := createHandlerTestAgent(t, "delegation-queue-source", nil)
	firstCallbackURL := "/api/v1/dispatch-tasks/router-queue-first/execution-result"
	firstUpdateURL := "/api/v1/dispatch-tasks/router-queue-first/execution-update"
	firstContext := fmt.Sprintf(`{
		"agent_identity_context_token":"first-queue-token",
		"completion_callback":{"url":%q,"update_url":%q,"target":%q}
	}`, firstCallbackURL, firstUpdateURL, testRouterTargetIdentity)
	firstSourceTaskID, _ := createDelegationSourceTask(t, sourceAgentID, firstContext)
	first := httptest.NewRecorder()
	testHandler.DelegateIssue(first, delegationRequest(t, firstSourceTaskID, sourceAgentID, map[string]any{
		"source_task_id": firstSourceTaskID,
		"mode":           "continue",
		"issue_id":       uuidToString(issue.ID),
		"content":        "把这些新闻撰写成日报卡片并发送给我",
	}))
	if first.Code != http.StatusCreated {
		t.Fatalf("first continuation = %d: %s", first.Code, first.Body.String())
	}
	var firstResponse IssueDelegationResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstResponse); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_task_queue
		SET runtime_mcp_overlay = '{"sentinel":"first-delegation-context"}'::jsonb
		WHERE id = $1
	`, firstResponse.TargetTaskID); err != nil {
		t.Fatal(err)
	}

	secondCallbackURL := "/api/v1/dispatch-tasks/router-queue-second/execution-result"
	secondUpdateURL := "/api/v1/dispatch-tasks/router-queue-second/execution-update"
	secondContext := fmt.Sprintf(`{
		"agent_identity_context_token":"second-queue-token",
		"completion_callback":{"url":%q,"update_url":%q,"target":%q}
	}`, secondCallbackURL, secondUpdateURL, testRouterTargetIdentity)
	secondSourceTaskID, _ := createDelegationSourceTask(t, sourceAgentID, secondContext)
	second := httptest.NewRecorder()
	testHandler.DelegateIssue(second, delegationRequest(t, secondSourceTaskID, sourceAgentID, map[string]any{
		"source_task_id": secondSourceTaskID,
		"mode":           "continue",
		"issue_id":       uuidToString(issue.ID),
		"content":        "这个任务别做了，取消掉",
	}))
	if second.Code != http.StatusCreated {
		t.Fatalf("second continuation = %d: %s", second.Code, second.Body.String())
	}
	var secondResponse IssueDelegationResponse
	if err := json.Unmarshal(second.Body.Bytes(), &secondResponse); err != nil {
		t.Fatal(err)
	}
	if firstResponse.TargetTaskID != secondResponse.TargetTaskID {
		t.Fatalf("continuations used different tasks: first=%s second=%s",
			firstResponse.TargetTaskID, secondResponse.TargetTaskID)
	}
	secondReplay := httptest.NewRecorder()
	testHandler.DelegateIssue(secondReplay, delegationRequest(t, secondSourceTaskID, sourceAgentID, map[string]any{
		"source_task_id": secondSourceTaskID,
		"mode":           "continue",
		"issue_id":       uuidToString(issue.ID),
		"content":        "这个任务别做了，取消掉",
	}))
	if secondReplay.Code != http.StatusOK {
		t.Fatalf("second continuation replay = %d: %s", secondReplay.Code, secondReplay.Body.String())
	}
	var secondReplayResponse IssueDelegationResponse
	if err := json.Unmarshal(secondReplay.Body.Bytes(), &secondReplayResponse); err != nil {
		t.Fatal(err)
	}
	if secondReplayResponse.TargetTaskID != secondResponse.TargetTaskID ||
		secondReplayResponse.TriggerCommentID != secondResponse.TriggerCommentID {
		t.Fatalf("second replay response=%+v, want target=%s comment=%s",
			secondReplayResponse, secondResponse.TargetTaskID, secondResponse.TriggerCommentID)
	}
	var replayCommentCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM comment
		WHERE author_type = 'member' AND source_task_id = $1
	`, secondSourceTaskID).Scan(&replayCommentCount); err != nil {
		t.Fatal(err)
	}
	if replayCommentCount != 1 {
		t.Fatalf("second continuation replay created %d comments, want 1", replayCommentCount)
	}

	var (
		parentTaskID         string
		targetContext        string
		targetRuntimeOverlay string
		triggerCommentID     string
		coalescedCommentIDs  []string
	)
	if err := testPool.QueryRow(context.Background(), `
		SELECT parent_task_id::text, context::text, runtime_mcp_overlay::text,
		       trigger_comment_id::text,
		       ARRAY(SELECT id::text FROM unnest(coalesced_comment_ids) AS id ORDER BY id::text)
		FROM agent_task_queue
		WHERE id = $1
	`, firstResponse.TargetTaskID).Scan(
		&parentTaskID,
		&targetContext,
		&targetRuntimeOverlay,
		&triggerCommentID,
		&coalescedCommentIDs,
	); err != nil {
		t.Fatal(err)
	}
	if parentTaskID != firstSourceTaskID {
		t.Fatalf("target parent task = %s, want first source %s", parentTaskID, firstSourceTaskID)
	}
	if !strings.Contains(targetContext, "first-queue-token") ||
		strings.Contains(targetContext, "second-queue-token") {
		t.Fatalf("coalesced task did not retain first context: %s", targetContext)
	}
	if !strings.Contains(targetRuntimeOverlay, "first-delegation-context") {
		t.Fatalf("coalesced task overwrote first runtime overlay: %s", targetRuntimeOverlay)
	}
	if triggerCommentID != secondResponse.TriggerCommentID ||
		len(coalescedCommentIDs) != 1 ||
		coalescedCommentIDs[0] != firstResponse.TriggerCommentID {
		t.Fatalf("comment plan trigger=%s coalesced=%v first=%s second=%s",
			triggerCommentID,
			coalescedCommentIDs,
			firstResponse.TriggerCommentID,
			secondResponse.TriggerCommentID,
		)
	}

	for _, mapping := range []struct {
		commentID    string
		sourceTaskID string
	}{
		{firstResponse.TriggerCommentID, firstSourceTaskID},
		{secondResponse.TriggerCommentID, secondSourceTaskID},
	} {
		var storedSourceTaskID string
		if err := testPool.QueryRow(context.Background(), `
			SELECT source_task_id::text FROM comment WHERE id = $1
		`, mapping.commentID).Scan(&storedSourceTaskID); err != nil {
			t.Fatal(err)
		}
		if storedSourceTaskID != mapping.sourceTaskID {
			t.Fatalf("comment %s source task = %s, want %s",
				mapping.commentID, storedSourceTaskID, mapping.sourceTaskID)
		}
	}

	for _, sourceTaskID := range []string{firstSourceTaskID, secondSourceTaskID} {
		sourceTaskUUID, _ := util.ParseUUID(sourceTaskID)
		if _, err := testHandler.TaskService.CompleteTask(
			context.Background(),
			sourceTaskUUID,
			[]byte(`{"output":"已转入后台处理"}`),
			"",
			"",
			false,
			"",
		); err != nil {
			t.Fatal(err)
		}
		var premature int
		if err := testPool.QueryRow(context.Background(), `
			SELECT count(*) FROM task_completion_outbox WHERE root_task_id = $1
		`, sourceTaskID).Scan(&premature); err != nil {
			t.Fatal(err)
		}
		if premature != 0 {
			t.Fatalf("source task %s queued terminal callback before issue task completion", sourceTaskID)
		}
	}

	targetTaskUUID, _ := util.ParseUUID(firstResponse.TargetTaskID)
	firstCommentUUID, _ := util.ParseUUID(firstResponse.TriggerCommentID)
	secondCommentUUID, _ := util.ParseUUID(secondResponse.TriggerCommentID)
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_task_queue
		SET status = 'running',
		    started_at = now(),
		    delivered_comment_ids = ARRAY[$2::uuid, $3::uuid]
		WHERE id = $1
	`, targetTaskUUID, firstCommentUUID, secondCommentUUID); err != nil {
		t.Fatal(err)
	}
	for _, reply := range []struct {
		parentID string
		content  string
	}{
		{firstResponse.TriggerCommentID, "日报卡片已生成并发送"},
		{secondResponse.TriggerCommentID, "已按追加指令停止后续发送"},
	} {
		if _, err := testPool.Exec(context.Background(), `
			INSERT INTO comment (
				issue_id, workspace_id, author_type, author_id, content, type,
				parent_id, source_task_id
			)
			VALUES ($1, $2, 'agent', $3, $4, 'comment', $5, $6)
		`, issue.ID, issue.WorkspaceID, targetAgentUUID, reply.content, reply.parentID, targetTaskUUID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := testHandler.TaskService.CompleteTask(
		context.Background(),
		targetTaskUUID,
		[]byte(`{"output":"本轮合并任务执行完成"}`),
		"",
		"",
		false,
		"",
	); err != nil {
		t.Fatal(err)
	}

	rows, err := testPool.Query(context.Background(), `
		SELECT root_task_id::text, callback_url, request_id, result_message
		FROM task_completion_outbox
		WHERE terminal_task_id = $1
		ORDER BY callback_url
	`, targetTaskUUID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]struct {
		rootTaskID    string
		requestID     string
		resultMessage string
	}{}
	for rows.Next() {
		var callbackURL, rootTaskID, requestID, resultMessage string
		if err := rows.Scan(&rootTaskID, &callbackURL, &requestID, &resultMessage); err != nil {
			t.Fatal(err)
		}
		got[callbackURL] = struct {
			rootTaskID    string
			requestID     string
			resultMessage string
		}{rootTaskID, requestID, resultMessage}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("terminal callback count = %d, want 2: %+v", len(got), got)
	}
	if firstResult := got[firstCallbackURL]; firstResult.rootTaskID != firstSourceTaskID ||
		firstResult.requestID != "multica-comment-terminal:"+firstSourceTaskID ||
		firstResult.resultMessage != "日报卡片已生成并发送" {
		t.Fatalf("first callback = %+v", firstResult)
	}
	if secondResult := got[secondCallbackURL]; secondResult.rootTaskID != secondSourceTaskID ||
		secondResult.requestID != "multica-comment-terminal:"+secondSourceTaskID ||
		secondResult.resultMessage != "已按追加指令停止后续发送" {
		t.Fatalf("second callback = %+v", secondResult)
	}
}

func createDelegationContinuationIssue(t *testing.T, targetAgentID, title string) db.Issue {
	t.Helper()
	targetAgentUUID, _ := util.ParseUUID(targetAgentID)
	workspaceUUID, _ := util.ParseUUID(testWorkspaceID)
	userUUID, _ := util.ParseUUID(testUserID)
	created, err := testHandler.IssueService.Create(context.Background(), service.IssueCreateParams{
		WorkspaceID:  workspaceUUID,
		Title:        title,
		Description:  pgtype.Text{String: "用于委派续接测试", Valid: true},
		Status:       "backlog",
		Priority:     "medium",
		AssigneeType: pgtype.Text{String: "agent", Valid: true},
		AssigneeID:   targetAgentUUID,
		CreatorType:  "member",
		CreatorID:    userUUID,
	}, service.IssueCreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `
		UPDATE issue SET status = 'todo' WHERE id = $1
	`, created.Issue.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, created.Issue.ID)
	})
	return created.Issue
}

func TestDelegateIssueContinuePersistsCommentTaskAndHandoffTogether(t *testing.T) {
	sourceAgentID := createHandlerTestAgent(t, "delegation-continue-source", nil)
	targetAgentID := createHandlerTestAgent(t, "delegation-continue-target", nil)
	issue := createDelegationContinuationIssue(t, targetAgentID, "复用已有科技新闻 Issue")
	callbackURL := "/api/v1/dispatch-tasks/router-continue/execution-result"
	updateURL := "/api/v1/dispatch-tasks/router-continue/execution-update"
	sourceContext := fmt.Sprintf(`{
		"completion_callback":{"url":%q,"update_url":%q,"target":%q}
	}`, callbackURL, updateURL, testRouterTargetIdentity)
	sourceTaskID, _ := createDelegationSourceTask(t, sourceAgentID, sourceContext)

	w := httptest.NewRecorder()
	testHandler.DelegateIssue(w, delegationRequest(t, sourceTaskID, sourceAgentID, map[string]any{
		"source_task_id": sourceTaskID,
		"mode":           "continue",
		"issue_id":       uuidToString(issue.ID),
		"content":        "把已有新闻整理成日报卡片",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("DelegateIssue continue = %d: %s", w.Code, w.Body.String())
	}
	var delegated IssueDelegationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &delegated); err != nil {
		t.Fatal(err)
	}
	var commentContent, targetTaskID, callbackPath string
	if err := testPool.QueryRow(context.Background(), `
		SELECT comment.content, execution_update.target_task_id::text,
		       execution_update.callback_url
		FROM task_execution_update_outbox execution_update
		JOIN agent_task_queue task ON task.id = execution_update.target_task_id
		JOIN comment ON comment.id = task.trigger_comment_id
		WHERE execution_update.root_task_id = $1
	`, sourceTaskID).Scan(&commentContent, &targetTaskID, &callbackPath); err != nil {
		t.Fatal(err)
	}
	if commentContent != "把已有新闻整理成日报卡片" ||
		targetTaskID != delegated.TargetTaskID ||
		callbackPath != updateURL {
		t.Fatalf("comment=%q target=%q callback=%q response=%+v",
			commentContent, targetTaskID, callbackPath, delegated)
	}
}

// The delegated follow-up task must carry the full attribution block, not just
// an originator. Migration 199 dropped the transitional originator_source IS
// NULL exemption, so a row with originator_user_id but no accountable_user_id
// is rejected by agent_task_queue_accountable_matches_originator and the whole
// continuation fails with a 500.
func TestDelegateIssueContinueStampsAccountableAttributionOnFollowUpTask(t *testing.T) {
	sourceAgentID := createHandlerTestAgent(t, "delegation-attribution-source", nil)
	targetAgentID := createHandlerTestAgent(t, "delegation-attribution-target", nil)
	issue := createDelegationContinuationIssue(t, targetAgentID, "委派续跑需要完整归因")
	sourceTaskID, _ := createDelegationSourceTask(t, sourceAgentID, `{}`)

	w := httptest.NewRecorder()
	testHandler.DelegateIssue(w, delegationRequest(t, sourceTaskID, sourceAgentID, map[string]any{
		"source_task_id": sourceTaskID,
		"mode":           "continue",
		"issue_id":       uuidToString(issue.ID),
		"content":        "继续这个 Issue 的后续工作",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("DelegateIssue continue = %d: %s", w.Code, w.Body.String())
	}
	var delegated IssueDelegationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &delegated); err != nil {
		t.Fatal(err)
	}

	var originator, accountable, source, delegatedFrom, evidenceKind, evidenceRef string
	if err := testPool.QueryRow(context.Background(), `
		SELECT COALESCE(originator_user_id::text, ''), COALESCE(accountable_user_id::text, ''),
		       COALESCE(originator_source, ''), COALESCE(delegated_from_task_id::text, ''),
		       COALESCE(trigger_evidence_kind, ''), COALESCE(trigger_evidence_ref_id::text, '')
		FROM agent_task_queue WHERE id = $1
	`, delegated.TargetTaskID).Scan(
		&originator, &accountable, &source, &delegatedFrom, &evidenceKind, &evidenceRef,
	); err != nil {
		t.Fatal(err)
	}
	if originator != testUserID || accountable != testUserID {
		t.Fatalf("originator=%q accountable=%q, both want the source task's human %q",
			originator, accountable, testUserID)
	}
	if source != "delegation" || delegatedFrom != sourceTaskID {
		t.Fatalf("originator_source=%q delegated_from_task_id=%q, want delegation from %q",
			source, delegatedFrom, sourceTaskID)
	}
	if evidenceKind != "comment" || evidenceRef != delegated.TriggerCommentID {
		t.Fatalf("evidence=%s/%s, want comment/%s", evidenceKind, evidenceRef, delegated.TriggerCommentID)
	}
}

func TestDelegateIssueContinuationWaitsUntilCommentIsDelivered(t *testing.T) {
	sourceAgentID := createHandlerTestAgent(t, "delegation-undelivered-source", nil)
	targetAgentID := createHandlerTestAgent(t, "delegation-undelivered-target", nil)
	issue := createDelegationContinuationIssue(t, targetAgentID, "委派评论等待实际交付")
	callbackURL := "/api/v1/dispatch-tasks/router-undelivered/execution-result"
	updateURL := "/api/v1/dispatch-tasks/router-undelivered/execution-update"
	sourceContext := fmt.Sprintf(`{
		"agent_identity_context_token":"undelivered-private-token",
		"completion_callback":{"url":%q,"update_url":%q,"target":%q}
	}`, callbackURL, updateURL, testRouterTargetIdentity)
	sourceTaskID, _ := createDelegationSourceTask(t, sourceAgentID, sourceContext)

	w := httptest.NewRecorder()
	testHandler.DelegateIssue(w, delegationRequest(t, sourceTaskID, sourceAgentID, map[string]any{
		"source_task_id": sourceTaskID,
		"mode":           "continue",
		"issue_id":       uuidToString(issue.ID),
		"content":        "这条评论必须等实际交付后才能回调",
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("DelegateIssue continue = %d: %s", w.Code, w.Body.String())
	}
	var delegated IssueDelegationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &delegated); err != nil {
		t.Fatal(err)
	}
	sourceTaskUUID, _ := util.ParseUUID(sourceTaskID)
	if _, err := testHandler.TaskService.CompleteTask(
		context.Background(),
		sourceTaskUUID,
		[]byte(`{"output":"已转入后台处理"}`),
		"",
		"",
		false,
		"",
	); err != nil {
		t.Fatal(err)
	}

	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_task_queue
		SET status = 'running', started_at = now(), delivered_comment_ids = '{}'
		WHERE id = $1
	`, delegated.TargetTaskID); err != nil {
		t.Fatal(err)
	}
	if completed := completeTaskViaHandler(t, delegated.TargetTaskID, "claim 未包含委派评论"); completed.Code != http.StatusOK {
		t.Fatalf("CompleteTask = %d: %s", completed.Code, completed.Body.String())
	}
	var premature int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM task_completion_outbox WHERE root_task_id = $1
	`, sourceTaskID).Scan(&premature); err != nil {
		t.Fatal(err)
	}
	if premature != 0 {
		t.Fatalf("undelivered delegated comment created %d terminal callbacks", premature)
	}
	var successorParentTaskID, successorTriggerCommentID, successorContext string
	if err := testPool.QueryRow(context.Background(), `
		SELECT parent_task_id::text, trigger_comment_id::text, context::text
		FROM agent_task_queue
		WHERE issue_id = $1 AND status = 'queued'
		ORDER BY created_at DESC
		LIMIT 1
	`, issue.ID).Scan(
		&successorParentTaskID,
		&successorTriggerCommentID,
		&successorContext,
	); err != nil {
		t.Fatal(err)
	}
	if successorParentTaskID != sourceTaskID ||
		successorTriggerCommentID != delegated.TriggerCommentID ||
		!strings.Contains(successorContext, "undelivered-private-token") {
		t.Fatalf("successor parent=%s trigger=%s context=%s",
			successorParentTaskID, successorTriggerCommentID, successorContext)
	}
}

func TestDelegateIssueContinueRollsBackCommentWhenTaskEnqueueFails(t *testing.T) {
	sourceAgentID := createHandlerTestAgent(t, "delegation-continue-rollback-source", nil)
	targetAgentID := createHandlerTestAgent(t, "delegation-continue-rollback-target", nil)
	issue := createDelegationContinuationIssue(t, targetAgentID, "续接失败回滚 Issue")
	sourceTaskID, _ := createDelegationSourceTask(t, sourceAgentID, `{}`)
	functionName := "test_fail_delegation_continue_enqueue"
	triggerName := "test_fail_delegation_continue_enqueue"
	if _, err := testPool.Exec(context.Background(), fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION %s()
		RETURNS trigger AS $$
		BEGIN
			RAISE EXCEPTION 'forced delegated continuation enqueue failure';
		END;
		$$ LANGUAGE plpgsql;
		DROP TRIGGER IF EXISTS %s ON agent_task_queue;
		CREATE TRIGGER %s
		BEFORE INSERT ON agent_task_queue
		FOR EACH ROW
		WHEN (NEW.parent_task_id = '%s'::uuid)
		EXECUTE FUNCTION %s()
	`, functionName, triggerName, triggerName, sourceTaskID, functionName)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), fmt.Sprintf(`
			DROP TRIGGER IF EXISTS %s ON agent_task_queue;
			DROP FUNCTION IF EXISTS %s()
		`, triggerName, functionName))
		_, _ = testPool.Exec(context.Background(), `
			DELETE FROM comment
			WHERE issue_id = $1 AND content = '失败续接不得留下评论'
		`, issue.ID)
	})

	w := httptest.NewRecorder()
	testHandler.DelegateIssue(w, delegationRequest(t, sourceTaskID, sourceAgentID, map[string]any{
		"source_task_id": sourceTaskID,
		"mode":           "continue",
		"issue_id":       uuidToString(issue.ID),
		"content":        "失败续接不得留下评论",
	}))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("DelegateIssue continue = %d: %s", w.Code, w.Body.String())
	}
	var commentCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM comment
		WHERE issue_id = $1 AND content = '失败续接不得留下评论'
	`, issue.ID).Scan(&commentCount); err != nil {
		t.Fatal(err)
	}
	if commentCount != 0 {
		t.Fatalf("delegation continuation failure left %d comments", commentCount)
	}
}

func TestCancelReleasedDelegationSourceCancelsBackgroundTaskAndReportsSource(t *testing.T) {
	sourceAgentID := createHandlerTestAgent(t, "delegation-cancel-source", nil)
	targetAgentID := createHandlerTestAgent(t, "delegation-cancel-target", nil)
	callbackURL := "/api/v1/dispatch-tasks/router-cancel/execution-result"
	updateURL := "/api/v1/dispatch-tasks/router-cancel/execution-update"
	sourceContext := fmt.Sprintf(`{
		"agent_identity_context_token":"private-cancel-token",
		"completion_callback":{"url":%q,"update_url":%q,"target":%q}
	}`, callbackURL, updateURL, testRouterTargetIdentity)
	sourceTaskID, _ := createDelegationSourceTask(t, sourceAgentID, sourceContext)

	req := delegationRequest(t, sourceTaskID, sourceAgentID, map[string]any{
		"source_task_id": sourceTaskID,
		"mode":           "create",
		"title":          "采集科技新闻并生成消息卡片",
		"description":    "在后台完成采集和发送。",
		"assignee_id":    targetAgentID,
	})
	w := httptest.NewRecorder()
	testHandler.DelegateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("DelegateIssue = %d: %s", w.Code, w.Body.String())
	}
	var delegated IssueDelegationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &delegated); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, delegated.IssueID)
	})

	sourceTaskUUID, _ := util.ParseUUID(sourceTaskID)
	if _, err := testHandler.TaskService.CompleteTask(
		context.Background(),
		sourceTaskUUID,
		[]byte(`{"output":"已转入后台处理"}`),
		"",
		"",
		false,
		"",
	); err != nil {
		t.Fatal(err)
	}

	targetTaskUUID, _ := util.ParseUUID(delegated.TargetTaskID)
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_task_queue
		SET status = 'failed', completed_at = now(), failure_reason = 'runtime_offline'
		WHERE id = $1
	`, targetTaskUUID); err != nil {
		t.Fatal(err)
	}
	retryTask, err := testHandler.Queries.CreateRetryTask(
		context.Background(),
		db.CreateRetryTaskParams{ID: targetTaskUUID},
	)
	if err != nil {
		t.Fatal(err)
	}

	cancelled, err := testHandler.TaskService.CancelTaskWithResult(
		context.Background(), sourceTaskUUID,
		service.CancelTaskOptions{ClientSupportsDraftRestore: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Task.Status != "completed" {
		t.Fatalf("released source status = %s, want completed", cancelled.Task.Status)
	}

	var targetStatus string
	if err := testPool.QueryRow(context.Background(), `
		SELECT status
		FROM agent_task_queue
		WHERE id = $1
	`, delegated.TargetTaskID).Scan(&targetStatus); err != nil {
		t.Fatal(err)
	}
	if targetStatus != "failed" {
		t.Fatalf("target status=%q, want failed", targetStatus)
	}
	var retryStatus string
	if err := testPool.QueryRow(context.Background(), `
		SELECT status
		FROM agent_task_queue
		WHERE id = $1
	`, retryTask.ID).Scan(&retryStatus); err != nil {
		t.Fatal(err)
	}
	if retryStatus != "cancelled" {
		t.Fatalf("retry status=%q, want cancelled", retryStatus)
	}

	var rootTaskID, terminalTaskID, storedCallbackURL, callbackAgentID, executionStatus, failureReason string
	if err := testPool.QueryRow(context.Background(), `
		SELECT root_task_id::text, terminal_task_id::text, callback_url,
		       agent_id::text, execution_status, failure_reason
		FROM task_completion_outbox
		WHERE root_task_id = $1
	`, sourceTaskID).Scan(
		&rootTaskID,
		&terminalTaskID,
		&storedCallbackURL,
		&callbackAgentID,
		&executionStatus,
		&failureReason,
	); err != nil {
		t.Fatal(err)
	}
	if rootTaskID != sourceTaskID ||
		terminalTaskID != uuidToString(retryTask.ID) ||
		storedCallbackURL != callbackURL ||
		callbackAgentID != sourceAgentID ||
		executionStatus != "canceled" ||
		failureReason != "cancelled" {
		t.Fatalf(
			"cancel completion root=%q terminal=%q (want %q) callback=%q agent=%q status=%q reason=%q",
			rootTaskID,
			terminalTaskID,
			uuidToString(retryTask.ID),
			storedCallbackURL,
			callbackAgentID,
			executionStatus,
			failureReason,
		)
	}
}

func TestDelegateIssueWithoutExternalCallbackStillFinalizesRelation(t *testing.T) {
	sourceAgentID := createHandlerTestAgent(t, "delegation-no-callback-source", nil)
	targetAgentID := createHandlerTestAgent(t, "delegation-no-callback-target", nil)
	sourceTaskID, _ := createDelegationSourceTask(t, sourceAgentID, `{}`)

	w := httptest.NewRecorder()
	testHandler.DelegateIssue(w, delegationRequest(t, sourceTaskID, sourceAgentID, map[string]any{
		"source_task_id": sourceTaskID,
		"mode":           "create",
		"title":          "整理技术调研结果",
		"description":    "在后台完成技术调研并记录结果。",
		"assignee_id":    targetAgentID,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("DelegateIssue = %d: %s", w.Code, w.Body.String())
	}
	var delegated IssueDelegationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &delegated); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, delegated.IssueID)
	})

	targetTaskID, _ := util.ParseUUID(delegated.TargetTaskID)
	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent_task_queue SET status = 'running', started_at = now() WHERE id = $1
	`, targetTaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := testHandler.TaskService.CompleteTask(
		context.Background(),
		targetTaskID,
		[]byte(`{"output":"调研完成"}`),
		"",
		"",
		false,
		"",
	); err != nil {
		t.Fatal(err)
	}

	var outboxCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM task_completion_outbox WHERE root_task_id = $1
	`, sourceTaskID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 0 {
		t.Fatalf("delegation without callback created %d outbox rows", outboxCount)
	}
}
