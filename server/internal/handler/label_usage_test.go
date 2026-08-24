package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/pkg/modelpricing"
)

func createLabelUsageTestIssue(t *testing.T, title string) IssueResponse {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/issues", map[string]any{
		"title":    title,
		"status":   "todo",
		"priority": "medium",
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create issue: status=%d body=%s", w.Code, w.Body.String())
	}
	var issue IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&issue); err != nil {
		t.Fatalf("decode issue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issue.ID)
	})
	return issue
}

func createLabelUsageTestLabel(t *testing.T, resourceType string) LabelResponse {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest(http.MethodPost, "/api/labels", map[string]any{
		"resource_type": resourceType,
		"name":          "usage-" + resourceType + "-" + uuid.NewString()[:8],
		"color":         "#2563eb",
	})
	testHandler.CreateLabel(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create label: status=%d body=%s", w.Code, w.Body.String())
	}
	var label LabelResponse
	if err := json.NewDecoder(w.Body).Decode(&label); err != nil {
		t.Fatalf("decode label: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue_to_label WHERE label_id = $1`, label.ID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue_label WHERE id = $1`, label.ID)
	})
	return label
}

func TestLabelUsageAggregatesAtTaskGrainAndPaginates(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	originalPricing := testHandler.cfg.ModelPricing
	testHandler.cfg.ModelPricing = modelpricing.Catalog{
		"unpriced-model": {Input: 1, Output: 1, CacheRead: 1, CacheWrite: 1},
	}
	t.Cleanup(func() { testHandler.cfg.ModelPricing = originalPricing })
	agentID := createHandlerTestAgent(t, "label-usage-"+uuid.NewString()[:8], []byte("[]"))
	label := createLabelUsageTestLabel(t, "issue")
	recentIssue := createLabelUsageTestIssue(t, "Recent label usage")
	oldIssue := createLabelUsageTestIssue(t, "Old label usage")

	for _, issueID := range []string{recentIssue.ID, oldIssue.ID} {
		if _, err := testPool.Exec(ctx, `
			INSERT INTO issue_to_label (issue_id, label_id)
			VALUES ($1, $2)
		`, issueID, label.ID); err != nil {
			t.Fatalf("attach label: %v", err)
		}
	}

	now := time.Now().UTC().Truncate(time.Second)
	oldAt := now.AddDate(0, 0, -120)
	insertTask := func(issueID string, createdAt time.Time) string {
		t.Helper()
		var taskID string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO agent_task_queue (
				agent_id, runtime_id, issue_id, status, priority,
				started_at, completed_at, created_at,
				originator_user_id, accountable_user_id, originator_source,
				trigger_evidence_kind, trigger_evidence_ref_id
			)
			VALUES ($1, $2, $3, 'completed', 0, $4, $4, $4,
			        $5, $5, 'direct_human', 'issue_assignment', $3)
			RETURNING id
		`, agentID, handlerTestRuntimeID(t), issueID, createdAt, testUserID).Scan(&taskID); err != nil {
			t.Fatalf("insert task: %v", err)
		}
		t.Cleanup(func() {
			_, _ = testPool.Exec(context.Background(), `DELETE FROM task_usage WHERE task_id = $1`, taskID)
			_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		})
		return taskID
	}
	recentTask := insertTask(recentIssue.ID, now)
	noUsageTask := insertTask(recentIssue.ID, now.Add(-time.Minute))
	oldTask := insertTask(oldIssue.ID, oldAt)

	if _, err := testPool.Exec(ctx, `
		INSERT INTO task_usage (
			task_id, provider, model,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
			cost_usd_ticks, created_at, updated_at
		)
		VALUES
			($1, 'OpenAI', 'priced-model',   10, 20, 30, 40, 500,  $2, $2),
			($1, 'OpenAI', 'unpriced-model',  1,  2,  3,  4, NULL, $2, $2),
			($3, 'Anthropic', 'old-model',   50, 50, 50, 50, 1000, $4, $4)
	`, recentTask, now, oldTask, oldAt); err != nil {
		t.Fatalf("insert usage: %v", err)
	}

	// Catalog callers do not pay for a workspace-wide historical usage scan by
	// default. Settings opts in explicitly and gets the all-time summary.
	w := httptest.NewRecorder()
	testHandler.ListLabels(w, newRequest(http.MethodGet, "/api/labels?resource_type=issue", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list labels: status=%d body=%s", w.Code, w.Body.String())
	}
	var list struct {
		Labels []LabelResponse `json:"labels"`
	}
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatalf("decode label list: %v", err)
	}
	for i := range list.Labels {
		if list.Labels[i].ID == label.ID && list.Labels[i].UsageSummary != nil {
			t.Fatalf("default label list unexpectedly returned usage summary: %+v", list.Labels[i].UsageSummary)
		}
	}

	w = httptest.NewRecorder()
	testHandler.ListLabels(w, newRequest(http.MethodGet, "/api/labels?resource_type=issue&include_usage=true", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list labels with usage: status=%d body=%s", w.Code, w.Body.String())
	}
	if err := json.NewDecoder(w.Body).Decode(&list); err != nil {
		t.Fatalf("decode label list with usage: %v", err)
	}
	var listed *LabelResponse
	for i := range list.Labels {
		if list.Labels[i].ID == label.ID {
			listed = &list.Labels[i]
			break
		}
	}
	if listed == nil || listed.UsageSummary == nil {
		t.Fatalf("label usage summary missing: %+v", listed)
	}
	if got := *listed.UsageSummary; got.TotalTokens != 310 || got.TotalCostUSDTicks != 101500 ||
		got.UncostedTokens != 0 || got.TaskCount != 3 ||
		got.PricedTaskCount != 2 || got.UnpricedTaskCount != 1 {
		t.Fatalf("list usage summary = %+v", got)
	}

	// Seven days excludes the old task, sorts the two recent tasks by cost,
	// and paginates by task rather than by the task's two model rows.
	req := newRequest(http.MethodGet,
		"/api/labels/"+label.ID+"/usage?period=7d&sort=cost&direction=desc&page=1&page_size=1&tz=UTC", nil)
	req = withURLParam(req, "id", label.ID)
	w = httptest.NewRecorder()
	testHandler.GetLabelUsage(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get label usage: status=%d body=%s", w.Code, w.Body.String())
	}
	var detail LabelUsageDetailResponse
	if err := json.NewDecoder(w.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Period != "7d" || detail.Timezone != "UTC" {
		t.Fatalf("period/timezone = %s/%s", detail.Period, detail.Timezone)
	}
	if got := detail.Summary; got.TotalTokens != 110 || got.TotalCostUSDTicks != 100500 ||
		got.UncostedTokens != 0 || got.TaskCount != 2 ||
		got.PricedTaskCount != 1 || got.UnpricedTaskCount != 1 {
		t.Fatalf("detail summary = %+v", got)
	}
	if len(detail.Tasks) != 1 || detail.Tasks[0].TaskID != recentTask {
		t.Fatalf("page 1 tasks = %+v, want recent priced task %s", detail.Tasks, recentTask)
	}
	if detail.Tasks[0].AgentID != agentID || detail.Tasks[0].AgentName == "" {
		t.Fatalf("task executor = %s/%q, want %s with name", detail.Tasks[0].AgentID, detail.Tasks[0].AgentName, agentID)
	}
	if detail.Tasks[0].Attribution == nil || detail.Tasks[0].Attribution.Source != "direct_human" ||
		!detail.Tasks[0].Attribution.Precise || detail.Tasks[0].Attribution.Initiator == nil ||
		detail.Tasks[0].Attribution.Initiator.ID != testUserID {
		t.Fatalf("task attribution = %+v", detail.Tasks[0].Attribution)
	}
	if detail.Tasks[0].Provider != "" || detail.Tasks[0].Model != "" {
		t.Fatalf("multi-model task must not claim one provider/model: %+v", detail.Tasks[0])
	}
	var taskModels []LabelUsageBreakdownResponse
	if err := json.Unmarshal(detail.Tasks[0].UsageBreakdown, &taskModels); err != nil || len(taskModels) != 2 {
		t.Fatalf("task usage breakdown = %s, err=%v", detail.Tasks[0].UsageBreakdown, err)
	}
	if detail.Pagination.Total != 2 || detail.Pagination.TotalPages != 2 {
		t.Fatalf("pagination = %+v", detail.Pagination)
	}
	if len(detail.Daily) != 1 || detail.Daily[0].TaskCount != 2 {
		t.Fatalf("daily = %+v", detail.Daily)
	}
	if len(detail.Breakdown) != 2 {
		t.Fatalf("breakdown rows = %d, want 2: %+v", len(detail.Breakdown), detail.Breakdown)
	}

	req = newRequest(http.MethodGet,
		"/api/labels/"+label.ID+"/usage?period=7d&sort=cost&direction=desc&page=2&page_size=1&tz=UTC", nil)
	req = withURLParam(req, "id", label.ID)
	w = httptest.NewRecorder()
	testHandler.GetLabelUsage(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get page 2: status=%d body=%s", w.Code, w.Body.String())
	}
	if err := json.NewDecoder(w.Body).Decode(&detail); err != nil {
		t.Fatalf("decode page 2: %v", err)
	}
	if len(detail.Tasks) != 1 || detail.Tasks[0].TaskID != noUsageTask || detail.Tasks[0].HasUsage || detail.Tasks[0].IsPriced {
		t.Fatalf("page 2 no-usage task = %+v, want %s", detail.Tasks, noUsageTask)
	}

	for _, tc := range []struct {
		sort      string
		direction string
		first     string
		second    string
	}{
		{sort: "cost", direction: "asc", first: noUsageTask, second: recentTask},
		{sort: "cost", direction: "desc", first: recentTask, second: noUsageTask},
		{sort: "tokens", direction: "asc", first: noUsageTask, second: recentTask},
		{sort: "tokens", direction: "desc", first: recentTask, second: noUsageTask},
		{sort: "recent", direction: "asc", first: noUsageTask, second: recentTask},
		{sort: "recent", direction: "desc", first: recentTask, second: noUsageTask},
	} {
		t.Run(tc.sort+"_"+tc.direction, func(t *testing.T) {
			req := newRequest(http.MethodGet,
				fmt.Sprintf("/api/labels/%s/usage?period=7d&sort=%s&direction=%s&page_size=10&tz=UTC", label.ID, tc.sort, tc.direction), nil)
			req = withURLParam(req, "id", label.ID)
			w := httptest.NewRecorder()
			testHandler.GetLabelUsage(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			var sorted LabelUsageDetailResponse
			if err := json.NewDecoder(w.Body).Decode(&sorted); err != nil {
				t.Fatalf("decode sorted response: %v", err)
			}
			if len(sorted.Tasks) != 2 || sorted.Tasks[0].TaskID != tc.first || sorted.Tasks[1].TaskID != tc.second {
				t.Fatalf("order=%v, want [%s %s]", sorted.Tasks, tc.first, tc.second)
			}
		})
	}

	for _, direction := range []string{"asc", "desc"} {
		t.Run("cost_"+direction+"_puts_fully_priced_first", func(t *testing.T) {
			req := newRequest(http.MethodGet,
				fmt.Sprintf("/api/labels/%s/usage?period=all&sort=cost&direction=%s&page_size=10&tz=UTC", label.ID, direction), nil)
			req = withURLParam(req, "id", label.ID)
			w := httptest.NewRecorder()
			testHandler.GetLabelUsage(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			var sorted LabelUsageDetailResponse
			if err := json.NewDecoder(w.Body).Decode(&sorted); err != nil {
				t.Fatalf("decode sorted response: %v", err)
			}
			if len(sorted.Tasks) != 3 || sorted.Tasks[0].TaskID != oldTask || !sorted.Tasks[0].IsPriced {
				t.Fatalf("fully priced task must precede partial/unknown costs: %+v", sorted.Tasks)
			}
		})
	}
}

func TestLabelUsageRejectsSkillLabelsAndInvalidQuery(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	skillLabel := createLabelUsageTestLabel(t, "skill")
	issueLabel := createLabelUsageTestLabel(t, "issue")

	tests := []struct {
		name   string
		label  string
		query  string
		status int
	}{
		{name: "skill label", label: skillLabel.ID, query: "period=30d", status: http.StatusNotFound},
		{name: "invalid period", label: issueLabel.ID, query: "period=8d", status: http.StatusBadRequest},
		{name: "invalid timezone", label: issueLabel.ID, query: "tz=Mars%2FOlympus", status: http.StatusBadRequest},
		{name: "invalid sort", label: issueLabel.ID, query: "sort=random", status: http.StatusBadRequest},
		{name: "invalid direction", label: issueLabel.ID, query: "direction=sideways", status: http.StatusBadRequest},
		{name: "invalid page size", label: issueLabel.ID, query: "page_size=101", status: http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := newRequest(http.MethodGet, fmt.Sprintf("/api/labels/%s/usage?%s", tc.label, tc.query), nil)
			req = withURLParam(req, "id", tc.label)
			w := httptest.NewRecorder()
			testHandler.GetLabelUsage(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}
