package handler

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type capturedRuntimeLaunch struct {
	task db.AgentTaskQueue
}

type captureRuntimeLauncher struct {
	calls chan capturedRuntimeLaunch
}

func TestHandleAgentDispatchRecordsMissingCredential(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler integration database is unavailable")
	}
	businessMetrics := obsmetrics.NewBusinessMetrics()
	originalMetrics := testHandler.Metrics
	testHandler.Metrics = businessMetrics
	t.Cleanup(func() { testHandler.Metrics = originalMetrics })

	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/agent-dispatch", strings.NewReader(`{}`))
	req = withURLParams(req, "endpointId", "v1_AAECAwQFBgcICQoLDA0ODw")
	w := httptest.NewRecorder()
	testHandler.HandleAgentDispatch(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing credential: expected 401, got %d: %s", w.Code, w.Body.String())
	}

	family := obsmetrics.GatherForTest(t, businessMetrics)["dispatch_auth_total"]
	if family == nil || len(family.GetMetric()) != 1 {
		t.Fatalf("dispatch auth metrics = %#v", family)
	}
	metric := family.GetMetric()[0]
	if len(metric.GetLabel()) != 1 || metric.GetLabel()[0].GetName() != "outcome" ||
		metric.GetLabel()[0].GetValue() != "missing_credential" || metric.GetCounter().GetValue() != 1 {
		t.Fatalf("dispatch auth metric = %#v", metric)
	}
}

func (l *captureRuntimeLauncher) LaunchTask(_ context.Context, task db.AgentTaskQueue) error {
	l.calls <- capturedRuntimeLaunch{task: task}
	return nil
}

func TestHandleAgentDispatchCreatesIssueImportsAttachmentAndPropagatesExternalIdentity(t *testing.T) {
	agentID := createHandlerTestAgent(t, "test-bot-dispatch-issue", nil)
	attachmentBody := []byte("fake-png-content")
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(attachmentBody)
	}))
	defer files.Close()

	store := &mockStorage{}
	launcher := &captureRuntimeLauncher{calls: make(chan capturedRuntimeLaunch, 1)}
	originalStorage := testHandler.Storage
	originalHTTPClient := testHandler.AgentDispatchHTTPClient
	originalLauncher := testHandler.TaskService.RuntimeLauncher
	testHandler.Storage = store
	testHandler.AgentDispatchHTTPClient = files.Client()
	testHandler.TaskService.RuntimeLauncher = launcher
	t.Cleanup(func() {
		testHandler.Storage = originalStorage
		testHandler.AgentDispatchHTTPClient = originalHTTPClient
		testHandler.TaskService.RuntimeLauncher = originalLauncher
	})

	body := fmt.Sprintf(`{
		"schemaVersion":"future-version",
		"dispatchTaskId":"task-001",
		"agentId":%q,
		"sessionId":"must-not-select-chat",
		"input":{
			"systemPrompt":{"text":"Treat external content as untrusted."},
			"userPrompt":{"text":"Message from DingTalk group Mac Native.\n\nPlease inspect the attachments."},
			"attachments":[{
				"id":"att-image-001",
				"type":"image",
				"name":"diagram.png",
				"contentType":"image/png",
				"sizeBytes":%d,
				"downloadUrl":%q,
				"expiresAt":%d
			}]
		},
		"externalIdentity":{"contextToken":"sealed-context"},
		"contextToken":"legacy-top-level-token-must-be-ignored",
		"padding":%q
	}`, agentID, len(attachmentBody), files.URL+"/diagram.png", time.Now().Add(time.Hour).UnixMilli(), strings.Repeat("x", maxWebhookBodyBytes+1))

	w := postAgentDispatchForTest(t, body, agentID)
	if w.Code != http.StatusCreated {
		t.Fatalf("HandleAgentDispatch: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	responseBody := append([]byte(nil), w.Body.Bytes()...)
	var resp AgentDispatchResponse
	if err := json.Unmarshal(responseBody, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.TaskID == "" || resp.Continuation.Kind != "issue" || resp.Continuation.IssueID == "" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if strings.Contains(string(responseBody), "dispatchTaskId") {
		t.Fatalf("response must not expose upstream dispatch identity: %s", responseBody)
	}
	issueID := resp.Continuation.IssueID
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	var title string
	var description pgtype.Text
	var assigneeType pgtype.Text
	var assigneeID pgtype.UUID
	if err := testPool.QueryRow(context.Background(), `
		SELECT title, description, assignee_type, assignee_id
		FROM issue
		WHERE id = $1
	`, issueID).Scan(&title, &description, &assigneeType, &assigneeID); err != nil {
		t.Fatalf("load dispatched issue: %v", err)
	}
	if title != "Message from DingTalk group Mac Native." {
		t.Fatalf("title = %q", title)
	}
	if !assigneeType.Valid || assigneeType.String != "agent" || uuidToString(assigneeID) != agentID {
		t.Fatalf("assignee = (%v, %s), want agent %s", assigneeType, uuidToString(assigneeID), agentID)
	}
	for _, want := range []string{"Please inspect the attachments.", "diagram.png"} {
		if !strings.Contains(description.String, want) {
			t.Errorf("description missing %q:\n%s", want, description.String)
		}
	}
	for _, forbidden := range []string{"Treat external content as untrusted.", "## System prompt", "## User prompt", "sealed-context", "legacy-top-level-token-must-be-ignored", "task-001", files.URL, "att-image-001"} {
		if strings.Contains(description.String, forbidden) {
			t.Errorf("description leaked %q:\n%s", forbidden, description.String)
		}
	}

	var filename, contentType, storedURL string
	var sizeBytes int64
	if err := testPool.QueryRow(context.Background(), `
		SELECT filename, content_type, size_bytes, url
		FROM attachment
		WHERE issue_id = $1
	`, issueID).Scan(&filename, &contentType, &sizeBytes, &storedURL); err != nil {
		t.Fatalf("load imported attachment: %v", err)
	}
	if filename != "diagram.png" || contentType != "image/png" || sizeBytes != int64(len(attachmentBody)) {
		t.Fatalf("unexpected attachment metadata: filename=%q contentType=%q size=%d", filename, contentType, sizeBytes)
	}
	store.mu.Lock()
	stored := append([]byte(nil), store.files[store.KeyFromURL(storedURL)]...)
	store.mu.Unlock()
	if string(stored) != string(attachmentBody) {
		t.Fatalf("stored attachment = %q, want %q", stored, attachmentBody)
	}

	select {
	case launch := <-launcher.calls:
		if uuidToString(launch.task.ID) != resp.TaskID {
			t.Fatalf("launched task = %s, want %s", uuidToString(launch.task.ID), resp.TaskID)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime launcher was not called")
	}

	var taskContext []byte
	var storedContextToken pgtype.Text
	if err := testPool.QueryRow(context.Background(), `
		SELECT context, context->>'agent_identity_context_token'
		FROM agent_task_queue
		WHERE id = $1
	`, resp.TaskID).Scan(&taskContext, &storedContextToken); err != nil {
		t.Fatalf("load task context: %v", err)
	}
	if !storedContextToken.Valid || storedContextToken.String != "sealed-context" {
		t.Fatalf("task context token = %#v, want trusted Router externalIdentity.contextToken", storedContextToken)
	}
	if strings.Contains(string(taskContext), "task-001") {
		t.Fatalf("task context leaked upstream dispatch identity: %s", taskContext)
	}
}

func TestHandleAgentDispatchIgnoresLegacyTopLevelContextToken(t *testing.T) {
	agentID := createHandlerTestAgent(t, "test-bot-dispatch-no-context-token", nil)
	body := fmt.Sprintf(`{
		"agentId":%q,
		"contextToken":"legacy-top-level-token-must-be-ignored",
		"input":{"userPrompt":{"text":"Create an issue using the selected agent."},"attachments":[]}
	}`, agentID)

	w := postAgentDispatchForTest(t, body, agentID)
	if w.Code != http.StatusCreated {
		t.Fatalf("HandleAgentDispatch with legacy top-level contextToken: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp AgentDispatchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, resp.Continuation.IssueID)
	})
	var hasRouterContextToken bool
	if err := testPool.QueryRow(context.Background(), `
		SELECT COALESCE(context ? 'agent_identity_context_token', false)
		FROM agent_task_queue
		WHERE id = $1
	`, resp.TaskID).Scan(&hasRouterContextToken); err != nil {
		t.Fatalf("load task context: %v", err)
	}
	if hasRouterContextToken {
		t.Fatal("task context unexpectedly contains a Router-provided identity token")
	}
}

func TestHandleAgentDispatchRequiresAgentIDWhenCreatingIssue(t *testing.T) {
	agentID := createHandlerTestAgent(t, "test-bot-dispatch-required-agent", nil)
	body := `{
		"input":{
			"systemPrompt":{"text":"External input."},
			"userPrompt":{"text":"Create a new issue without an agent."},
			"attachments":[]
		}
	}`
	w := postAgentDispatchForTest(t, body, agentID)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("HandleAgentDispatch missing agentId: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "agentId is required") {
		t.Fatalf("HandleAgentDispatch missing agentId: unexpected response %s", w.Body.String())
	}
}

func TestHandleAgentDispatchContinuationCreatesIssueComment(t *testing.T) {
	agentID := createHandlerTestAgent(t, "test-bot-dispatch-comment", nil)
	created, err := testHandler.IssueService.Create(context.Background(), service.IssueCreateParams{
		WorkspaceID:    parseUUID(testWorkspaceID),
		Title:          "External topic",
		Description:    pgtype.Text{String: "Initial external event", Valid: true},
		Status:         "backlog",
		Priority:       "none",
		AssigneeType:   pgtype.Text{String: "agent", Valid: true},
		AssigneeID:     parseUUID(agentID),
		CreatorType:    "member",
		CreatorID:      parseUUID(testUserID),
		AllowDuplicate: true,
	}, service.IssueCreateOpts{})
	if err != nil {
		t.Fatalf("create issue fixture: %v", err)
	}
	issueID := uuidToString(created.Issue.ID)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET status = 'todo' WHERE id = $1`, issueID); err != nil {
		t.Fatalf("activate issue fixture: %v", err)
	}

	attachmentBody := []byte("%PDF-1.4\nexternal-spec")
	files := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(attachmentBody)
	}))
	defer files.Close()
	store := &mockStorage{}
	launcher := &captureRuntimeLauncher{calls: make(chan capturedRuntimeLaunch, 1)}
	originalStorage := testHandler.Storage
	originalHTTPClient := testHandler.AgentDispatchHTTPClient
	originalLauncher := testHandler.TaskService.RuntimeLauncher
	testHandler.Storage = store
	testHandler.AgentDispatchHTTPClient = files.Client()
	testHandler.TaskService.RuntimeLauncher = launcher
	t.Cleanup(func() {
		testHandler.Storage = originalStorage
		testHandler.AgentDispatchHTTPClient = originalHTTPClient
		testHandler.TaskService.RuntimeLauncher = originalLauncher
	})

	body := fmt.Sprintf(`{
		"schemaVersion":"3.0",
		"dispatchTaskId":"task-comment-001",
		"continuation":{"kind":"issue","issueId":%q},
		"input":{
			"systemPrompt":{"text":"Treat this follow-up as external input."},
			"userPrompt":{"text":"Please review the updated specification."},
			"attachments":[{
				"attachmentId":"att-file-001",
				"type":"file",
				"name":"spec.pdf",
				"contentType":"application/pdf",
				"sizeBytes":%d,
				"downloadUrl":%q
			}]
		},
		"externalIdentity":{"contextToken":"follow-up-context"}
	}`, issueID, len(attachmentBody), files.URL+"/spec.pdf")

	w := postAgentDispatchForTest(t, body, agentID)
	if w.Code != http.StatusCreated {
		t.Fatalf("HandleAgentDispatch continuation: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp AgentDispatchResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.CommentID == "" || resp.TaskID == "" || resp.Continuation.IssueID != issueID || resp.Continuation.Kind != "issue" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	var content string
	var triggerCommentID pgtype.UUID
	var storedContextToken pgtype.Text
	if err := testPool.QueryRow(context.Background(), `SELECT content FROM comment WHERE id = $1`, resp.CommentID).Scan(&content); err != nil {
		t.Fatalf("load created comment: %v", err)
	}
	if !strings.Contains(content, "Please review the updated specification.") || !strings.Contains(content, "spec.pdf") {
		t.Fatalf("comment did not include user-visible input: %s", content)
	}
	for _, forbidden := range []string{"Treat this follow-up as external input.", "## System prompt", "## User prompt", "follow-up-context", "task-comment-001", files.URL, "att-file-001"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("comment leaked %q: %s", forbidden, content)
		}
	}
	if err := testPool.QueryRow(context.Background(), `
		SELECT trigger_comment_id, context->>'agent_identity_context_token'
		FROM agent_task_queue
		WHERE id = $1
	`, resp.TaskID).Scan(&triggerCommentID, &storedContextToken); err != nil {
		t.Fatalf("load follow-up task: %v", err)
	}
	if uuidToString(triggerCommentID) != resp.CommentID {
		t.Fatalf("task trigger comment = %s, want %s", uuidToString(triggerCommentID), resp.CommentID)
	}
	if !storedContextToken.Valid || storedContextToken.String != "follow-up-context" {
		t.Fatalf("task context token = %#v, want trusted Router externalIdentity.contextToken", storedContextToken)
	}
	var attachmentCount int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM attachment WHERE comment_id = $1`, resp.CommentID).Scan(&attachmentCount); err != nil {
		t.Fatalf("count comment attachments: %v", err)
	}
	if attachmentCount != 1 {
		t.Fatalf("comment attachment count = %d, want 1", attachmentCount)
	}

	select {
	case <-launcher.calls:
	case <-time.After(time.Second):
		t.Fatal("runtime launcher was not called for follow-up")
	}
}

func TestHandleAgentDispatchRecreatesMissingContinuationIssue(t *testing.T) {
	agentID := createHandlerTestAgent(t, "test-bot-dispatch-missing-continuation", nil)
	const missingIssueID = "00000000-0000-4000-8000-000000000001"
	body := fmt.Sprintf(`{
		"continuation":{"kind":"issue","issueId":%q},
		"input":{
			"systemPrompt":{"text":"External input."},
			"userPrompt":{"text":"Continue after the original issue was deleted."},
			"attachments":[]
		}
	}`, missingIssueID)

	w := postAgentDispatchForTest(t, body, agentID)
	if w.Code != http.StatusCreated {
		t.Fatalf("HandleAgentDispatch missing continuation: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp AgentDispatchResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.TaskID == "" || resp.Continuation.Kind != "issue" || resp.Continuation.IssueID == "" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.Continuation.IssueID == missingIssueID {
		t.Fatalf("continuation issue id was not refreshed: %+v", resp.Continuation)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, resp.Continuation.IssueID)
	})

	var issueExists bool
	if err := testPool.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM issue WHERE id = $1)`, resp.Continuation.IssueID).Scan(&issueExists); err != nil {
		t.Fatalf("check recreated issue: %v", err)
	}
	if !issueExists {
		t.Fatal("recreated continuation issue does not exist")
	}
}

func TestHandleAgentDispatchContinuationRejectsRunningIssueTask(t *testing.T) {
	agentID := createHandlerTestAgent(t, "test-bot-dispatch-running", nil)
	created, err := testHandler.IssueService.Create(context.Background(), service.IssueCreateParams{
		WorkspaceID:    parseUUID(testWorkspaceID),
		Title:          "Busy external topic",
		Description:    pgtype.Text{String: "Initial external event", Valid: true},
		Status:         "backlog",
		Priority:       "none",
		AssigneeType:   pgtype.Text{String: "agent", Valid: true},
		AssigneeID:     parseUUID(agentID),
		CreatorType:    "member",
		CreatorID:      parseUUID(testUserID),
		AllowDuplicate: true,
	}, service.IssueCreateOpts{})
	if err != nil {
		t.Fatalf("create issue fixture: %v", err)
	}
	issue := created.Issue
	issueID := uuidToString(issue.ID)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})
	if _, err := testPool.Exec(context.Background(), `UPDATE issue SET status = 'todo' WHERE id = $1`, issueID); err != nil {
		t.Fatalf("activate issue fixture: %v", err)
	}
	issue.Status = "todo"

	originalLauncher := testHandler.TaskService.RuntimeLauncher
	testHandler.TaskService.RuntimeLauncher = nil
	t.Cleanup(func() { testHandler.TaskService.RuntimeLauncher = originalLauncher })
	task, err := testHandler.TaskService.EnqueueTaskForIssue(context.Background(), issue)
	if err != nil {
		t.Fatalf("enqueue running task fixture: %v", err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status = 'running' WHERE id = $1`, task.ID); err != nil {
		t.Fatalf("mark task running: %v", err)
	}

	body := fmt.Sprintf(`{
		"dispatchTaskId":"upstream-only",
		"continuation":{"kind":"issue","issueId":%q},
		"input":{
			"systemPrompt":{"text":"External input."},
			"userPrompt":{"text":"Follow up while the previous run is active."},
			"attachments":[]
		}
	}`, issueID)
	w := postAgentDispatchForTest(t, body, agentID)
	if w.Code != http.StatusConflict {
		t.Fatalf("HandleAgentDispatch running continuation: expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleAgentDispatchRejectsMemberWithoutAgentInvocationPermission(t *testing.T) {
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	var memberID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ('dispatch outsider', $1)
		RETURNING id
	`, fmt.Sprintf("dispatch-outsider-%d@multica.test", suffix)).Scan(&memberID); err != nil {
		t.Fatalf("create dispatch member: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'member')
	`, testWorkspaceID, memberID); err != nil {
		t.Fatalf("create workspace member: %v", err)
	}
	var agentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, permission_mode, max_concurrent_tasks, owner_id,
			instructions, custom_env, custom_args
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 'private', 1, $4, '', '{}'::jsonb, '[]'::jsonb)
		RETURNING id
	`, testWorkspaceID, fmt.Sprintf("private-dispatch-agent-%d", suffix), handlerTestRuntimeID(t), testUserID).Scan(&agentID); err != nil {
		t.Fatalf("create private dispatch agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE creator_id = $1`, memberID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, memberID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, memberID)
	})

	body := fmt.Sprintf(`{
		"agentId":%q,
		"input":{
			"systemPrompt":{"text":"External input."},
			"userPrompt":{"text":"Attempt to invoke a private agent."},
			"attachments":[]
		}
	}`, agentID)
	endpointID, deliverySecret := createAgentDispatchEndpointForTest(t, memberID, agentID)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/agent-dispatch", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+deliverySecret)
	req = withURLParams(req, "endpointId", endpointID)
	w := httptest.NewRecorder()
	testHandler.HandleAgentDispatch(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("HandleAgentDispatch private agent: expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleAgentDispatchRejectsContinuationOutsideEndpointAgent(t *testing.T) {
	boundAgentID := createHandlerTestAgent(t, "test-bot-dispatch-bound-continuation", nil)
	otherAgentID := createHandlerTestAgent(t, "test-bot-dispatch-other-continuation", nil)
	created, err := testHandler.IssueService.Create(context.Background(), service.IssueCreateParams{
		WorkspaceID:    parseUUID(testWorkspaceID),
		Title:          "Another agent's external topic",
		Description:    pgtype.Text{String: "Initial external event", Valid: true},
		Status:         "backlog",
		Priority:       "none",
		AssigneeType:   pgtype.Text{String: "agent", Valid: true},
		AssigneeID:     parseUUID(otherAgentID),
		CreatorType:    "member",
		CreatorID:      parseUUID(testUserID),
		AllowDuplicate: true,
	}, service.IssueCreateOpts{})
	if err != nil {
		t.Fatalf("create issue fixture: %v", err)
	}
	issueID := uuidToString(created.Issue.ID)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	endpointID, deliverySecret := createAgentDispatchEndpointForTest(t, testUserID, boundAgentID)
	body := fmt.Sprintf(`{
		"continuation":{"kind":"issue","issueId":%q},
		"input":{"userPrompt":{"text":"Do not cross endpoint scope."},"attachments":[]}
	}`, issueID)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/agent-dispatch", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+deliverySecret)
	req = withURLParams(req, "endpointId", endpointID)
	w := httptest.NewRecorder()
	testHandler.HandleAgentDispatch(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("HandleAgentDispatch continuation endpoint agent: expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleAgentDispatchRejectsMissingOrInvalidDeliverySecret(t *testing.T) {
	agentID := createHandlerTestAgent(t, "test-bot-dispatch-auth", nil)
	endpointID, _ := createAgentDispatchEndpointForTest(t, testUserID, agentID)
	body := fmt.Sprintf(`{
		"agentId":%q,
		"input":{"userPrompt":{"text":"Authenticated delivery only."},"attachments":[]}
	}`, agentID)

	for _, tc := range []struct {
		name          string
		authorization string
	}{
		{name: "missing secret"},
		{name: "invalid secret", authorization: "Bearer wrong-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/agent-dispatch", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}
			req = withURLParams(req, "endpointId", endpointID)
			w := httptest.NewRecorder()
			testHandler.HandleAgentDispatch(w, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("HandleAgentDispatch auth: expected 401, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestHandleAgentDispatchRejectsMalformedEndpointWithoutParsingOracle(t *testing.T) {
	agentID := createHandlerTestAgent(t, "test-bot-dispatch-malformed-endpoint", nil)
	_, deliverySecret := createAgentDispatchEndpointForTest(t, testUserID, agentID)
	body := fmt.Sprintf(`{
		"agentId":%q,
		"input":{"userPrompt":{"text":"Authenticated delivery only."},"attachments":[]}
	}`, agentID)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/agent-dispatch", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+deliverySecret)
	req = withURLParams(req, "endpointId", "not-a-versioned-endpoint")
	w := httptest.NewRecorder()
	testHandler.HandleAgentDispatch(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("malformed endpoint: expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleAgentDispatchRejectsAgentOutsideEndpoint(t *testing.T) {
	boundAgentID := createHandlerTestAgent(t, "test-bot-dispatch-bound", nil)
	requestedAgentID := createHandlerTestAgent(t, "test-bot-dispatch-other", nil)
	endpointID, deliverySecret := createAgentDispatchEndpointForTest(t, testUserID, boundAgentID)
	body := fmt.Sprintf(`{
		"agentId":%q,
		"input":{"userPrompt":{"text":"Do not cross endpoint scope."},"attachments":[]}
	}`, requestedAgentID)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/agent-dispatch", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+deliverySecret)
	req = withURLParams(req, "endpointId", endpointID)
	w := httptest.NewRecorder()
	testHandler.HandleAgentDispatch(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("HandleAgentDispatch endpoint agent: expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func postAgentDispatchForTest(t *testing.T, body, agentID string) *httptest.ResponseRecorder {
	t.Helper()
	endpointID, deliverySecret := createAgentDispatchEndpointForTest(t, testUserID, agentID)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/agent-dispatch", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+deliverySecret)
	req = withURLParams(req, "endpointId", endpointID)
	w := httptest.NewRecorder()
	testHandler.HandleAgentDispatch(w, req)
	return w
}

func createAgentDispatchEndpointForTest(t *testing.T, actorUserID, agentID string) (string, string) {
	t.Helper()
	keyring, err := agentmessagerouter.ParseDispatchKeyring(
		"v1:ERERERERERERERERERERERERERERERERERERERERERE",
		"v1",
	)
	if err != nil {
		t.Fatalf("parse dispatch keyring: %v", err)
	}
	endpointID, err := keyring.GenerateEndpointID(rand.Reader)
	if err != nil {
		t.Fatalf("generate dispatch endpoint: %v", err)
	}
	deliverySecret, err := keyring.DeriveDeliverySecret(endpointID)
	if err != nil {
		t.Fatalf("derive dispatch credential: %v", err)
	}
	dispatchURL, err := agentmessagerouter.BuildDispatchURL("https://multica.example", endpointID)
	if err != nil {
		t.Fatalf("build dispatch URL: %v", err)
	}
	var dispatchEndpointID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_dispatch_endpoint (
			workspace_id, agent_id, actor_user_id, endpoint_id, dispatch_url
		)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id
	`, testWorkspaceID, agentID, actorUserID, endpointID, dispatchURL).Scan(&dispatchEndpointID); err != nil {
		t.Fatalf("create agent dispatch endpoint: %v", err)
	}
	previousKeyring := testHandler.AgentDispatchKeys
	testHandler.AgentDispatchKeys = keyring
	t.Cleanup(func() {
		testHandler.AgentDispatchKeys = previousKeyring
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_dispatch_endpoint WHERE id = $1`, dispatchEndpointID)
	})
	return endpointID, deliverySecret
}
