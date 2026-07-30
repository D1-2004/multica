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
			initiator_user_id, originator_user_id, context, started_at
		)
		VALUES ($1, $2, NULL, 'running', 2, $3, $4, $4, $5::jsonb, now())
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
		"dispatch_idempotency_key":"dispatch-window:delegation",
		"parent_ref":{"type":"router_task","id":"router-task-123"},
		"unknown_large_number":9007199254740993,
		"agent_identity_context_token":"private-context-token",
		"agent_identity_context_token_expires_at":4102444800,
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
	for _, inherited := range []string{"private-context-token", "dispatch_schema_version", "parent_ref", "router-task-123", "completion_callback", "9007199254740993"} {
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
		handoffStatus != "queued" {
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
		[]byte(`{"output":"已转入后台处理"}`),
		"chat-session-runtime",
		"",
	); err != nil {
		t.Fatal(err)
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

func TestDelegateIssueContinueReusesExternalIssueDispatchBusySemantics(t *testing.T) {
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
	sourceTaskID, _ := createDelegationSourceTask(t, sourceAgentID, `{}`)
	req := delegationRequest(t, sourceTaskID, sourceAgentID, map[string]any{
		"source_task_id": sourceTaskID,
		"mode":           "continue",
		"issue_id":       uuidToString(issue.ID),
		"content":        "把这些新闻撰写成日报卡片并发送给我",
	})
	w := httptest.NewRecorder()
	testHandler.DelegateIssue(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("continue with active issue task = %d: %s", w.Code, w.Body.String())
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

	cancelled, err := testHandler.TaskService.CancelTaskWithResult(context.Background(), sourceTaskUUID)
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
		executionStatus != "failed" ||
		failureReason != "cancelled" {
		t.Fatalf(
			"cancel completion root=%q terminal=%q callback=%q agent=%q status=%q reason=%q",
			rootTaskID,
			terminalTaskID,
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
