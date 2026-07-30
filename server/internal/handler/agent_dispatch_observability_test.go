package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type agentDispatchObservabilityFixture struct {
	taskID        string
	endpointID    string
	deliveryToken string
	startedAt     time.Time
	completedAt   time.Time
}

func TestAgentDispatchObservabilitySummaryReturnsExistingTaskData(t *testing.T) {
	fixture := createAgentDispatchObservabilityFixture(t, "summary")

	request := agentDispatchObservabilityRequest(
		t,
		http.MethodGet,
		fmt.Sprintf(
			"/api/webhooks/agent-dispatch/%s/tasks/%s/summary",
			fixture.endpointID,
			fixture.taskID,
		),
		fixture,
		fixture.taskID,
	)
	response := httptest.NewRecorder()

	testHandler.GetAgentDispatchTaskSummary(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("summary status = %d, want 200: %s", response.Code, response.Body.String())
	}
	var body struct {
		TaskID              string  `json:"task_id"`
		Status              string  `json:"status"`
		StartedAt           *string `json:"started_at"`
		CompletedAt         *string `json:"completed_at"`
		DurationMS          *int64  `json:"duration_ms"`
		Provider            *string `json:"provider"`
		Model               *string `json:"model"`
		InputTokens         *int64  `json:"input_tokens"`
		OutputTokens        *int64  `json:"output_tokens"`
		CacheReadTokens     *int64  `json:"cache_read_tokens"`
		CacheWriteTokens    *int64  `json:"cache_write_tokens"`
		MessageCount        int32   `json:"message_count"`
		ToolCallCount       int32   `json:"tool_call_count"`
		TranscriptAvailable bool    `json:"transcript_available"`
		UsageDetails        []struct {
			Provider         string `json:"provider"`
			Model            string `json:"model"`
			InputTokens      int64  `json:"input_tokens"`
			OutputTokens     int64  `json:"output_tokens"`
			CacheReadTokens  int64  `json:"cache_read_tokens"`
			CacheWriteTokens int64  `json:"cache_write_tokens"`
		} `json:"usage_details"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode summary response: %v", err)
	}
	if body.TaskID != fixture.taskID || body.Status != "completed" {
		t.Fatalf("summary identity/status = %s/%s", body.TaskID, body.Status)
	}
	if body.StartedAt == nil || *body.StartedAt != fixture.startedAt.Format(time.RFC3339Nano) {
		t.Fatalf("started_at = %v, want %s", body.StartedAt, fixture.startedAt.Format(time.RFC3339Nano))
	}
	if body.CompletedAt == nil || *body.CompletedAt != fixture.completedAt.Format(time.RFC3339Nano) {
		t.Fatalf("completed_at = %v, want %s", body.CompletedAt, fixture.completedAt.Format(time.RFC3339Nano))
	}
	if body.DurationMS == nil || *body.DurationMS != 3000 {
		t.Fatalf("duration_ms = %v, want 3000", body.DurationMS)
	}
	if body.Provider == nil || *body.Provider != "anthropic" ||
		body.Model == nil || *body.Model != "claude-sonnet-4" {
		t.Fatalf("provider/model = %v/%v", body.Provider, body.Model)
	}
	if body.InputTokens == nil || *body.InputTokens != 101 ||
		body.OutputTokens == nil || *body.OutputTokens != 29 ||
		body.CacheReadTokens == nil || *body.CacheReadTokens != 17 ||
		body.CacheWriteTokens == nil || *body.CacheWriteTokens != 3 {
		t.Fatalf(
			"token totals = input:%v output:%v cache_read:%v cache_write:%v",
			body.InputTokens,
			body.OutputTokens,
			body.CacheReadTokens,
			body.CacheWriteTokens,
		)
	}
	if body.MessageCount != 3 || body.ToolCallCount != 1 || !body.TranscriptAvailable {
		t.Fatalf(
			"message summary = messages:%d tools:%d transcript:%v",
			body.MessageCount,
			body.ToolCallCount,
			body.TranscriptAvailable,
		)
	}
	if len(body.UsageDetails) != 1 || body.UsageDetails[0].Provider != "anthropic" ||
		body.UsageDetails[0].Model != "claude-sonnet-4" {
		t.Fatalf("usage_details = %#v", body.UsageDetails)
	}
}

func TestAgentDispatchObservabilitySummaryHidesAnotherAgentTask(t *testing.T) {
	fixture := createAgentDispatchObservabilityFixture(t, "scope-owner")
	other := createAgentDispatchObservabilityFixture(t, "scope-other")

	request := agentDispatchObservabilityRequest(
		t,
		http.MethodGet,
		fmt.Sprintf(
			"/api/webhooks/agent-dispatch/%s/tasks/%s/summary",
			fixture.endpointID,
			other.taskID,
		),
		fixture,
		other.taskID,
	)
	response := httptest.NewRecorder()

	testHandler.GetAgentDispatchTaskSummary(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-agent summary status = %d, want 404: %s", response.Code, response.Body.String())
	}
}

func TestAgentDispatchObservabilitySummaryReturnsNullForMissingUsage(t *testing.T) {
	fixture := createAgentDispatchObservabilityFixture(t, "missing-usage")
	if _, err := testPool.Exec(
		context.Background(),
		`DELETE FROM task_usage WHERE task_id = $1`,
		fixture.taskID,
	); err != nil {
		t.Fatalf("delete usage fixture: %v", err)
	}

	request := agentDispatchObservabilityRequest(
		t,
		http.MethodGet,
		fmt.Sprintf(
			"/api/webhooks/agent-dispatch/%s/tasks/%s/summary",
			fixture.endpointID,
			fixture.taskID,
		),
		fixture,
		fixture.taskID,
	)
	response := httptest.NewRecorder()
	testHandler.GetAgentDispatchTaskSummary(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("missing usage summary status = %d, want 200: %s", response.Code, response.Body.String())
	}
	var body struct {
		Provider         *string `json:"provider"`
		Model            *string `json:"model"`
		InputTokens      *int64  `json:"input_tokens"`
		OutputTokens     *int64  `json:"output_tokens"`
		CacheReadTokens  *int64  `json:"cache_read_tokens"`
		CacheWriteTokens *int64  `json:"cache_write_tokens"`
		UsageDetails     []any   `json:"usage_details"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode missing usage summary: %v", err)
	}
	if body.Provider != nil || body.Model != nil || body.InputTokens != nil ||
		body.OutputTokens != nil || body.CacheReadTokens != nil || body.CacheWriteTokens != nil {
		t.Fatalf("missing usage fields are not null: %#v", body)
	}
	if body.UsageDetails == nil || len(body.UsageDetails) != 0 {
		t.Fatalf("usage_details = %#v, want []", body.UsageDetails)
	}
}

func TestAgentDispatchObservabilityMessagesPaginatesExistingPayload(t *testing.T) {
	fixture := createAgentDispatchObservabilityFixture(t, "messages")

	firstRequest := agentDispatchObservabilityRequest(
		t,
		http.MethodGet,
		fmt.Sprintf(
			"/api/webhooks/agent-dispatch/%s/tasks/%s/messages?since=0&limit=2",
			fixture.endpointID,
			fixture.taskID,
		),
		fixture,
		fixture.taskID,
	)
	firstResponse := httptest.NewRecorder()
	testHandler.ListAgentDispatchTaskMessages(firstResponse, firstRequest)

	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first messages status = %d, want 200: %s", firstResponse.Code, firstResponse.Body.String())
	}
	var firstPage struct {
		Items      []protocol.TaskMessagePayload `json:"items"`
		NextCursor *string                       `json:"next_cursor"`
	}
	if err := json.Unmarshal(firstResponse.Body.Bytes(), &firstPage); err != nil {
		t.Fatalf("decode first messages page: %v", err)
	}
	if len(firstPage.Items) != 2 || firstPage.Items[0].Seq != 1 || firstPage.Items[1].Seq != 2 {
		t.Fatalf("first page items = %#v", firstPage.Items)
	}
	if firstPage.Items[1].Type != "tool_use" || firstPage.Items[1].Tool != "Search" ||
		firstPage.Items[1].Input["query"] != "safe" {
		t.Fatalf("tool payload = %#v", firstPage.Items[1])
	}
	if firstPage.NextCursor == nil || *firstPage.NextCursor != "2" {
		t.Fatalf("first next_cursor = %v, want 2", firstPage.NextCursor)
	}

	secondRequest := agentDispatchObservabilityRequest(
		t,
		http.MethodGet,
		fmt.Sprintf(
			"/api/webhooks/agent-dispatch/%s/tasks/%s/messages?since=2&limit=2",
			fixture.endpointID,
			fixture.taskID,
		),
		fixture,
		fixture.taskID,
	)
	secondResponse := httptest.NewRecorder()
	testHandler.ListAgentDispatchTaskMessages(secondResponse, secondRequest)

	if secondResponse.Code != http.StatusOK {
		t.Fatalf("second messages status = %d, want 200: %s", secondResponse.Code, secondResponse.Body.String())
	}
	var secondPage struct {
		Items      []protocol.TaskMessagePayload `json:"items"`
		NextCursor *string                       `json:"next_cursor"`
	}
	if err := json.Unmarshal(secondResponse.Body.Bytes(), &secondPage); err != nil {
		t.Fatalf("decode second messages page: %v", err)
	}
	if len(secondPage.Items) != 1 || secondPage.Items[0].Seq != 3 ||
		secondPage.Items[0].Output != "tool finished" {
		t.Fatalf("second page items = %#v", secondPage.Items)
	}
	if secondPage.NextCursor != nil {
		t.Fatalf("second next_cursor = %v, want null", secondPage.NextCursor)
	}
}

func TestAgentDispatchObservabilityRequiresDispatchCredential(t *testing.T) {
	fixture := createAgentDispatchObservabilityFixture(t, "missing-credential")
	request := agentDispatchObservabilityRequest(
		t,
		http.MethodGet,
		fmt.Sprintf(
			"/api/webhooks/agent-dispatch/%s/tasks/%s/summary",
			fixture.endpointID,
			fixture.taskID,
		),
		fixture,
		fixture.taskID,
	)
	request.Header.Del("Authorization")
	response := httptest.NewRecorder()

	testHandler.GetAgentDispatchTaskSummary(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing credential status = %d, want 401: %s", response.Code, response.Body.String())
	}
}

func createAgentDispatchObservabilityFixture(
	t *testing.T,
	name string,
) agentDispatchObservabilityFixture {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("handler integration database is unavailable")
	}

	agentID := createHandlerTestAgent(t, "observability-"+name, nil)
	endpointID, deliveryToken := createAgentDispatchEndpointForTest(t, testUserID, agentID)
	startedAt := time.Date(2026, 7, 29, 6, 0, 2, 123000000, time.UTC)
	completedAt := startedAt.Add(3 * time.Second)

	created, err := testHandler.IssueService.Create(context.Background(), service.IssueCreateParams{
		WorkspaceID:    parseUUID(testWorkspaceID),
		Title:          "observability " + name,
		Status:         "backlog",
		Priority:       "none",
		AssigneeType:   pgtype.Text{String: "agent", Valid: true},
		AssigneeID:     parseUUID(agentID),
		CreatorType:    "member",
		CreatorID:      parseUUID(testUserID),
		AllowDuplicate: true,
	}, service.IssueCreateOpts{})
	if err != nil {
		t.Fatalf("create observability issue: %v", err)
	}
	issueID := uuidToString(created.Issue.ID)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})

	var taskID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_task_queue (
			agent_id, issue_id, status, priority, runtime_id,
			dispatched_at, started_at, completed_at, created_at
		)
		VALUES ($1, $2, 'completed', 0, $3, $4, $5, $6, $7)
		RETURNING id
	`,
		agentID,
		issueID,
		handlerTestRuntimeID(t),
		startedAt.Add(-time.Second),
		startedAt,
		completedAt,
		startedAt.Add(-2*time.Second),
	).Scan(&taskID); err != nil {
		t.Fatalf("create observability task: %v", err)
	}

	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO task_usage (
			task_id, provider, model, input_tokens, output_tokens,
			cache_read_tokens, cache_write_tokens
		)
		VALUES ($1, 'anthropic', 'claude-sonnet-4', 101, 29, 17, 3)
	`, taskID); err != nil {
		t.Fatalf("create observability usage: %v", err)
	}
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO task_message (
			task_id, seq, type, tool, content, input, output, created_at
		)
		VALUES
			($1, 1, 'text', NULL, 'working', NULL, NULL, $2),
			($1, 2, 'tool_use', 'Search', NULL, '{"query":"safe"}'::jsonb, NULL, $3),
			($1, 3, 'tool_result', 'Search', NULL, NULL, 'tool finished', $4)
	`, taskID, startedAt, startedAt.Add(time.Second), startedAt.Add(2*time.Second)); err != nil {
		t.Fatalf("create observability messages: %v", err)
	}

	return agentDispatchObservabilityFixture{
		taskID:        taskID,
		endpointID:    endpointID,
		deliveryToken: deliveryToken,
		startedAt:     startedAt,
		completedAt:   completedAt,
	}
}

func agentDispatchObservabilityRequest(
	t *testing.T,
	method string,
	target string,
	fixture agentDispatchObservabilityFixture,
	taskID string,
) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, target, nil)
	request.Header.Set("Authorization", "Bearer "+fixture.deliveryToken)
	return withURLParams(
		request,
		"endpointId",
		fixture.endpointID,
		"taskId",
		taskID,
	)
}
