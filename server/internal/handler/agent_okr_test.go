package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/modelpricing"
)

func setAgentOKRsForTest(t *testing.T, agentID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.SetAgentOKRs(w, withURLParam(newRequestAs(
		testUserID, http.MethodPut, "/api/agents/"+agentID+"/okrs", body,
	), "id", agentID))
	return w
}

func decodeAgentOKRs(t *testing.T, w *httptest.ResponseRecorder) AgentOKRResponse {
	t.Helper()
	var response AgentOKRResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode OKRs: %v", err)
	}
	return response
}

// The whole point of the feature: each objective and key result becomes a real
// workspace label in the issue namespace, so the agent can attach it to an
// Issue through the ordinary labeling path.
func TestSetAgentOKRs_MaterializesAttachableIssueLabels(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "okr-labels", nil)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_okr WHERE agent_id = $1`, agentID)
		_, _ = testPool.Exec(context.Background(),
			`DELETE FROM issue_label WHERE description = $1`, agentOKRLabelDescription)
	})

	w := setAgentOKRsForTest(t, agentID, map[string]any{
		"okrs": []map[string]any{
			{"objective": "提升交付效率", "key_results": []string{"平均响应<2h", "一次通过率>90%"}},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("set OKRs: got %d: %s", w.Code, w.Body.String())
	}
	response := decodeAgentOKRs(t, w)
	if len(response.OKRs) != 1 || len(response.OKRs[0].KeyResults) != 2 {
		t.Fatalf("unexpected OKR shape: %+v", response.OKRs)
	}
	if response.OKRs[0].Label != "O: 提升交付效率" {
		t.Errorf("objective label = %q", response.OKRs[0].Label)
	}
	if response.OKRs[0].ID == "" || response.OKRs[0].LabelID == "" || response.OKRs[0].Position != 0 {
		t.Errorf("objective identity = %+v", response.OKRs[0])
	}
	if response.OKRs[0].KeyResults[0].Label != "KR: 平均响应<2h" {
		t.Errorf("key result label = %q", response.OKRs[0].KeyResults[0].Label)
	}
	if response.OKRs[0].KeyResults[0].ID == "" || response.OKRs[0].KeyResults[0].LabelID == "" ||
		response.OKRs[0].KeyResults[0].Position != 0 {
		t.Errorf("key result identity = %+v", response.OKRs[0].KeyResults[0])
	}
	if !response.UsageAvailable {
		t.Error("usage_available = false, want successful spend query")
	}

	// The namespace matters: an 'agent'-namespace label cannot be attached to
	// an issue, which would make the feature useless.
	rows, err := testPool.Query(context.Background(), `
		SELECT label.resource_type
		FROM agent_okr okr JOIN issue_label label ON label.id = okr.label_id
		WHERE okr.agent_id = $1`, agentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var resourceType string
		if err := rows.Scan(&resourceType); err != nil {
			t.Fatal(err)
		}
		if resourceType != "issue" {
			t.Errorf("OKR label landed in the %q namespace; it cannot be attached to an issue", resourceType)
		}
		count++
	}
	if count != 3 {
		t.Errorf("materialized %d labels, want 3 (1 objective + 2 key results)", count)
	}
}

// A rewrite replaces the set. Labels are deliberately left in the catalog:
// deleting one would strip the tag off issues already labeled with it.
func TestSetAgentOKRs_RewriteReplacesTheSetAndKeepsLabels(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "okr-rewrite", nil)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_okr WHERE agent_id = $1`, agentID)
		_, _ = testPool.Exec(context.Background(),
			`DELETE FROM issue_label WHERE description = $1`, agentOKRLabelDescription)
	})

	if w := setAgentOKRsForTest(t, agentID, map[string]any{
		"okrs": []map[string]any{{"objective": "第一目标", "key_results": []string{"KR 甲"}}},
	}); w.Code != http.StatusOK {
		t.Fatalf("first write: got %d: %s", w.Code, w.Body.String())
	}
	w := setAgentOKRsForTest(t, agentID, map[string]any{
		"okrs": []map[string]any{{"objective": "第二目标", "key_results": []string{"KR 乙"}}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("rewrite: got %d: %s", w.Code, w.Body.String())
	}
	response := decodeAgentOKRs(t, w)
	if len(response.OKRs) != 1 || response.OKRs[0].Objective != "第二目标" {
		t.Fatalf("rewrite did not replace the set: %+v", response.OKRs)
	}

	var orphanedLabelStillExists bool
	if err := testPool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM issue_label WHERE name = 'O: 第一目标')`,
	).Scan(&orphanedLabelStillExists); err != nil {
		t.Fatal(err)
	}
	if !orphanedLabelStillExists {
		t.Errorf("rewrite deleted a label that issues may already carry")
	}
}

func TestSetAgentOKRs_PreservesAuthoredObjectiveOrder(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "okr-order", nil)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_okr WHERE agent_id = $1`, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue_label WHERE description = $1`, agentOKRLabelDescription)
	})

	w := setAgentOKRsForTest(t, agentID, map[string]any{"okrs": []map[string]any{
		{"objective": "First", "key_results": []string{"First KR"}},
		{"objective": "Second", "key_results": []string{"Second KR"}},
		{"objective": "Third", "key_results": []string{}},
	}})
	if w.Code != http.StatusOK {
		t.Fatalf("set OKRs: got %d: %s", w.Code, w.Body.String())
	}
	got := decodeAgentOKRs(t, w).OKRs
	if len(got) != 3 || got[0].Objective != "First" || got[1].Objective != "Second" || got[2].Objective != "Third" {
		t.Fatalf("objective order = %+v", got)
	}
	for i := range got {
		if got[i].Position != int32(i) {
			t.Errorf("objective %d position = %d", i, got[i].Position)
		}
	}
}

func TestSetAgentOKRs_ReusesOnlyItsCurrentLabels(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "okr-owned-label", nil)
	name := "Owned " + agentID[:8]
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_okr WHERE agent_id = $1`, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue_label WHERE name = $1`, agentOKRObjectivePrefix+name)
	})

	first := setAgentOKRsForTest(t, agentID, map[string]any{
		"okrs": []map[string]any{{"objective": name, "key_results": []string{}}},
	})
	if first.Code != http.StatusOK {
		t.Fatalf("first write: got %d: %s", first.Code, first.Body.String())
	}
	firstLabelID := decodeAgentOKRs(t, first).OKRs[0].LabelID
	second := setAgentOKRsForTest(t, agentID, map[string]any{
		"okrs": []map[string]any{{"objective": name, "key_results": []string{}}},
	})
	if second.Code != http.StatusOK {
		t.Fatalf("rewrite: got %d: %s", second.Code, second.Body.String())
	}
	if got := decodeAgentOKRs(t, second).OKRs[0].LabelID; got != firstLabelID {
		t.Fatalf("label_id changed: %s -> %s", firstLabelID, got)
	}
}

func TestSetAgentOKRs_RejectsAnUnownedSameNameLabel(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "okr-label-conflict", nil)
	name := "Conflict " + agentID[:8]
	labelName := agentOKRObjectivePrefix + name
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO issue_label (workspace_id, resource_type, name, description, color)
		VALUES ($1, 'issue', $2, 'ordinary label', '#000000')
	`, testWorkspaceID, labelName); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_okr WHERE agent_id = $1`, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue_label WHERE workspace_id = $1 AND name = $2`, testWorkspaceID, labelName)
	})

	w := setAgentOKRsForTest(t, agentID, map[string]any{
		"okrs": []map[string]any{{"objective": name, "key_results": []string{}}},
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", w.Code, w.Body.String())
	}
	var description string
	if err := testPool.QueryRow(context.Background(), `
		SELECT description FROM issue_label WHERE workspace_id = $1 AND name = $2
	`, testWorkspaceID, labelName).Scan(&description); err != nil {
		t.Fatal(err)
	}
	if description != "ordinary label" {
		t.Fatalf("conflicting label was overwritten: %q", description)
	}
}

func TestSetAgentOKRs_RejectsALegacyLabelSharedWithAnotherAgent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "okr-shared-a", nil)
	otherAgentID := createHandlerTestAgent(t, "okr-shared-b", nil)
	name := "Shared " + agentID[:8]
	first := setAgentOKRsForTest(t, agentID, map[string]any{
		"okrs": []map[string]any{{"objective": name, "key_results": []string{}}},
	})
	if first.Code != http.StatusOK {
		t.Fatalf("first write: got %d: %s", first.Code, first.Body.String())
	}
	labelID := decodeAgentOKRs(t, first).OKRs[0].LabelID
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO agent_okr (workspace_id, agent_id, kind, label_id, position)
		VALUES ($1, $2, 'objective', $3, 0)
	`, testWorkspaceID, otherAgentID, labelID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_okr WHERE agent_id = $1`, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_okr WHERE agent_id = $1`, otherAgentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue_label WHERE id = $1`, labelID)
	})

	rewrite := setAgentOKRsForTest(t, agentID, map[string]any{
		"okrs": []map[string]any{{"objective": name, "key_results": []string{}}},
	})
	if rewrite.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rewrite.Code, rewrite.Body.String())
	}
}

func TestAgentOKRReferencedLabelRejectsGenericUpdateAndDelete(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "okr-protected-label", nil)
	name := "Protected " + agentID[:8]
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_okr WHERE agent_id = $1`, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue_label WHERE name = $1`, agentOKRObjectivePrefix+name)
	})
	w := setAgentOKRsForTest(t, agentID, map[string]any{
		"okrs": []map[string]any{{"objective": name, "key_results": []string{}}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("set OKRs: got %d: %s", w.Code, w.Body.String())
	}
	labelID := decodeAgentOKRs(t, w).OKRs[0].LabelID

	updateReq := withURLParam(newRequest(http.MethodPut, "/api/labels/"+labelID, map[string]any{"name": "renamed"}), "id", labelID)
	update := httptest.NewRecorder()
	testHandler.UpdateLabel(update, updateReq)
	if update.Code != http.StatusConflict {
		t.Fatalf("update status = %d, want 409: %s", update.Code, update.Body.String())
	}

	deleteReq := withURLParam(newRequest(http.MethodDelete, "/api/labels/"+labelID, nil), "id", labelID)
	deleted := httptest.NewRecorder()
	testHandler.DeleteLabel(deleted, deleteReq)
	if deleted.Code != http.StatusConflict {
		t.Fatalf("delete status = %d, want 409: %s", deleted.Code, deleted.Body.String())
	}
}

// Clearing is a rewrite to an empty list.
func TestSetAgentOKRs_EmptyListClearsTheSet(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "okr-clear", nil)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_okr WHERE agent_id = $1`, agentID)
		_, _ = testPool.Exec(context.Background(),
			`DELETE FROM issue_label WHERE description = $1`, agentOKRLabelDescription)
	})

	if w := setAgentOKRsForTest(t, agentID, map[string]any{
		"okrs": []map[string]any{{"objective": "目标", "key_results": []string{"KR"}}},
	}); w.Code != http.StatusOK {
		t.Fatalf("write: got %d: %s", w.Code, w.Body.String())
	}
	w := setAgentOKRsForTest(t, agentID, map[string]any{"okrs": []map[string]any{}})
	if w.Code != http.StatusOK {
		t.Fatalf("clear: got %d: %s", w.Code, w.Body.String())
	}
	if okrs := decodeAgentOKRs(t, w).OKRs; len(okrs) != 0 {
		t.Fatalf("clear left %d OKRs", len(okrs))
	}
}

func TestSetAgentOKRs_RejectsInvalidInput(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "okr-invalid", nil)

	for name, body := range map[string]map[string]any{
		"blank objective": {"okrs": []map[string]any{{"objective": "   ", "key_results": []string{}}}},
		"oversized objective": {"okrs": []map[string]any{
			{"objective": strings.Repeat("中", maxAgentOKRTextLength+1), "key_results": []string{}},
		}},
		"oversized key result": {"okrs": []map[string]any{
			{"objective": "目标", "key_results": []string{strings.Repeat("中", maxAgentOKRTextLength+1)}},
		}},
		"duplicate objectives": {"okrs": []map[string]any{
			{"objective": "Same", "key_results": []string{}},
			{"objective": "same", "key_results": []string{}},
		}},
		"duplicate key results": {"okrs": []map[string]any{
			{"objective": "Goal", "key_results": []string{"Same", "same"}},
		}},
	} {
		if w := setAgentOKRsForTest(t, agentID, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400: %s", name, w.Code, w.Body.String())
		}
	}

	var stored int
	if err := testPool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM agent_okr WHERE agent_id = $1`, agentID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Errorf("a rejected write persisted %d rows", stored)
	}
}

// Objectives lead and tagging follows. The first version framed the whole
// section as a tagging vocabulary, which told the model what to write on an
// Issue but nothing about what it was trying to achieve.
func TestBuildAgentOKRInstructionsLeadsWithObjectives(t *testing.T) {
	t.Parallel()

	instructions := buildAgentOKRInstructions([]AgentOKRDTO{{
		Objective: "提升交付效率",
		Label:     "O: 提升交付效率",
		KeyResults: []AgentOKRKeyResultDTO{
			{Text: "平均响应<2h", Label: "KR: 平均响应<2h"},
		},
	}})

	objectives := strings.Index(instructions, "## Objectives")
	tagging := strings.Index(instructions, "### Tagging")
	if objectives < 0 || tagging < 0 {
		t.Fatalf("expected both an Objectives and a Tagging section:\n%s", instructions)
	}
	if objectives > tagging {
		t.Errorf("tagging precedes the objectives it derives from:\n%s", instructions)
	}
	// The objective must appear as its own text, not only inside a label
	// string — the point is that the agent knows the goal, not the tag.
	if !strings.Contains(instructions, "**提升交付效率**") {
		t.Errorf("objective is not stated as a goal:\n%s", instructions)
	}
	if !strings.Contains(instructions, "accountable for") {
		t.Errorf("objectives section does not say what they are for:\n%s", instructions)
	}
	// Objectives inform judgment; they must not read as an override of the task.
	if !strings.Contains(instructions, "the task wins") {
		t.Errorf("objectives section is missing its precedence clause:\n%s", instructions)
	}
}

// The tagging half still has to be exact: an agent told only "tag the issue"
// invents labels and the catalog fragments.
func TestBuildAgentOKRInstructionsNamesExactLabelsAndCommand(t *testing.T) {
	t.Parallel()

	instructions := buildAgentOKRInstructions([]AgentOKRDTO{{
		Objective: "提升交付效率",
		Label:     "O: 提升交付效率",
		KeyResults: []AgentOKRKeyResultDTO{
			{Text: "平均响应<2h", Label: "KR: 平均响应<2h"},
		},
	}})

	for _, want := range []string{
		"`O: 提升交付效率`",
		"`KR: 平均响应<2h`",
		"multica issue label add",
		"do not invent",
	} {
		if !strings.Contains(instructions, want) {
			t.Errorf("OKR instructions missing %q:\n%s", want, instructions)
		}
	}
}

// An agent with no objectives must read a prompt that never mentions them —
// no heading, no empty list, nothing.
func TestBuildAgentOKRInstructionsInjectsNothingWithoutOKRs(t *testing.T) {
	t.Parallel()

	for name, okrs := range map[string][]AgentOKRDTO{
		"nil":   nil,
		"empty": {},
	} {
		if got := buildAgentOKRInstructions(okrs); got != "" {
			t.Errorf("%s: expected no OKR section at all, got %q", name, got)
		}
	}
}

func TestAgentOKRDTOOmitsSpendWhenUsageIsUnavailable(t *testing.T) {
	t.Parallel()
	rows := []db.ListAgentOKRsRow{{
		ID:      parseUUID("11111111-1111-1111-1111-111111111111"),
		LabelID: parseUUID("22222222-2222-2222-2222-222222222222"),
		Kind:    "objective", Position: 0,
		LabelName: "O: Visible", LabelColor: "#6366f1",
	}}
	okrs := agentOKRsFromRows(rows, nil, false)
	if len(okrs) != 1 || okrs[0].Spend != nil {
		t.Fatalf("OKRs = %+v", okrs)
	}
	payload, err := json.Marshal(AgentOKRResponse{OKRs: okrs, UsageAvailable: false})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), `"spend"`) || !strings.Contains(string(payload), `"usage_available":false`) {
		t.Fatalf("payload = %s", payload)
	}
}

// The claim path must not inject a section for an agent that never configured
// one. Checked end to end rather than only on the builder, because the empty
// case is decided by the loader.
func TestAgentOKRInstructionsAreAbsentForAnAgentWithoutOKRs(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "okr-none", nil)

	got := testHandler.agentOKRInstructionsFor(
		context.Background(), parseUUID(agentID), parseUUID(testWorkspaceID))
	if got != "" {
		t.Fatalf("agent without OKRs received an objectives section:\n%s", got)
	}
}

// Spend is rolled up per label in one query. task_usage is unique per
// (task, provider, model), so a task that used two models has two rows: tokens
// and cost sum across them, but it is still one task.
func TestAgentOKRSpendRollsUpPerLabelWithoutDoubleCounting(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "okr-spend", nil)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_okr WHERE agent_id = $1`, agentID)
		_, _ = testPool.Exec(context.Background(),
			`DELETE FROM issue_label WHERE description = $1`, agentOKRLabelDescription)
	})

	if w := setAgentOKRsForTest(t, agentID, map[string]any{
		"okrs": []map[string]any{{"objective": "省钱目标", "key_results": []string{"KR 成本"}}},
	}); w.Code != http.StatusOK {
		t.Fatalf("set OKRs: got %d: %s", w.Code, w.Body.String())
	}

	var krLabelID string
	if err := testPool.QueryRow(context.Background(),
		`SELECT id FROM issue_label WHERE name = 'KR: KR 成本'`).Scan(&krLabelID); err != nil {
		t.Fatal(err)
	}

	// One issue carrying the key-result label, one task on it, two usage rows.
	var issueID, taskID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO issue (workspace_id, title, status, priority, assignee_type, assignee_id,
		                   creator_type, creator_id, number, position)
		VALUES ($1, '成本任务', 'todo', 'none', 'agent', $2, 'member', $3,
		        (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1), 0)
		RETURNING id`, testWorkspaceID, agentID, testUserID).Scan(&issueID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
	})
	if _, err := testPool.Exec(context.Background(),
		`INSERT INTO issue_to_label (issue_id, label_id) VALUES ($1, $2)`, issueID, krLabelID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(context.Background(),
		`INSERT INTO agent_task_queue (agent_id, issue_id, status, completed_at)
		 VALUES ($1, $2, 'completed', now()) RETURNING id`,
		agentID, issueID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"model-a", "model-b"} {
		if _, err := testPool.Exec(context.Background(), `
			INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens,
			                        cache_read_tokens, cache_write_tokens, cost_usd_ticks)
			VALUES ($1, 'test', $2, 100, 50, 0, 0, 1000)`, taskID, model); err != nil {
			t.Fatal(err)
		}
	}

	spend, usageAvailable := testHandler.agentOKRSpendFor(
		context.Background(), parseUUID(agentID), parseUUID(testWorkspaceID))
	if !usageAvailable {
		t.Fatal("usage query unexpectedly unavailable")
	}
	got := spend[krLabelID]

	if got.TaskCount != 1 {
		t.Errorf("task_count = %d, want 1 (one task, not one row per model)", got.TaskCount)
	}
	if got.TotalTokens != 300 {
		t.Errorf("total_tokens = %d, want 300 (two models × 150)", got.TotalTokens)
	}
	if got.TotalCostUSDTicks != 2000 {
		t.Errorf("total_cost_usd_ticks = %d, want 2000 (two models × 1000)", got.TotalCostUSDTicks)
	}
	if got.UnpricedTaskCount != 0 {
		t.Errorf("unpriced_task_count = %d, want 0 — every row carried a price", got.UnpricedTaskCount)
	}

	// The objective's own label carries no Issue, so it reports nothing rather
	// than inheriting its key result's spend.
	var objLabelID string
	if err := testPool.QueryRow(context.Background(),
		`SELECT id FROM issue_label WHERE name = 'O: 省钱目标'`).Scan(&objLabelID); err != nil {
		t.Fatal(err)
	}
	if objSpend := spend[objLabelID]; objSpend.TaskCount != 0 || objSpend.TotalCostUSDTicks != 0 {
		t.Errorf("objective inherited its key result's spend: %+v", objSpend)
	}
}

func TestAgentOKRSpendMatchesLabelUsageWithDiamondPricing(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	originalPricing := testHandler.cfg.ModelPricing
	testHandler.cfg.ModelPricing = modelpricing.Catalog{
		"qwen3.8-max":  {Input: 1, Output: 2, CacheRead: 0.1, CacheWrite: 0.5},
		"qwen3.7-plus": {Input: 0.5, Output: 1, CacheRead: 0.05, CacheWrite: 0.25},
	}
	t.Cleanup(func() { testHandler.cfg.ModelPricing = originalPricing })

	agentID := createHandlerTestAgent(t, "okr-diamond-pricing", nil)
	objective := "计价一致"
	keyResult := "标签与 OKR 同价"
	if w := setAgentOKRsForTest(t, agentID, map[string]any{
		"okrs": []map[string]any{{"objective": objective, "key_results": []string{keyResult}}},
	}); w.Code != http.StatusOK {
		t.Fatalf("set OKRs: got %d: %s", w.Code, w.Body.String())
	}

	var labelID, issueID, taskID string
	if err := testPool.QueryRow(ctx,
		`SELECT id FROM issue_label WHERE workspace_id = $1 AND name = $2`,
		testWorkspaceID, agentOKRKeyResultPrefix+keyResult).Scan(&labelID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, assignee_type, assignee_id,
		                   creator_type, creator_id, number, position)
		VALUES ($1, 'OKR Diamond 计价', 'todo', 'none', 'agent', $2, 'member', $3,
		        (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1), 0)
		RETURNING id`, testWorkspaceID, agentID, testUserID).Scan(&issueID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM task_usage WHERE task_id = $1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue_to_label WHERE issue_id = $1`, issueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_okr WHERE agent_id = $1`, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue_label WHERE description = $1`, agentOKRLabelDescription)
	})
	if _, err := testPool.Exec(ctx,
		`INSERT INTO issue_to_label (issue_id, label_id) VALUES ($1, $2)`, issueID, labelID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx,
		`INSERT INTO agent_task_queue (agent_id, issue_id, status, completed_at)
		 VALUES ($1, $2, 'completed', now()) RETURNING id`,
		agentID, issueID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO task_usage (
			task_id, provider, model, input_tokens, output_tokens,
			cache_read_tokens, cache_write_tokens, cost_usd_ticks
		) VALUES
			($1, 'runtime-exact',           'qwen3.8-max',                    1000000,       0, 0, 0, NULL),
			($1, 'runtime-custom',          'custom:qwen3.7-plus',                  0, 1000000, 0, 0, NULL),
			($1, 'runtime-provider',        'opencode/qwen3.8-max',           1000000,       0, 0, 0, NULL),
			($1, 'runtime-provider-custom', 'opencode/custom:qwen3.7-plus',         0, 1000000, 0, 0, NULL),
			($1, 'provider',                'qwen3.8-max',                    1000000,       0, 0, 0, 777),
			($1, 'runtime-unknown',         'opencode/qwen3.8-max-preview',       123,       0, 0, 0, NULL)
	`, taskID); err != nil {
		t.Fatal(err)
	}

	spend, usageAvailable := testHandler.agentOKRSpendFor(
		ctx, parseUUID(agentID), parseUUID(testWorkspaceID))
	if !usageAvailable {
		t.Fatal("Agent OKR usage query unexpectedly unavailable")
	}
	okr := spend[labelID]
	pricingJSON, err := testHandler.cfg.ModelPricing.SQLJSON()
	if err != nil {
		t.Fatal(err)
	}
	label, err := testHandler.Queries.GetIssueLabelUsageSummary(ctx, db.GetIssueLabelUsageSummaryParams{
		LabelID:      parseUUID(labelID),
		WorkspaceID:  parseUUID(testWorkspaceID),
		Since:        pgtype.Timestamptz{},
		ModelPricing: pricingJSON,
	})
	if err != nil {
		t.Fatal(err)
	}
	if okr.TotalTokens != label.TotalTokens ||
		okr.TotalCostUSDTicks != label.TotalCostUsdTicks ||
		okr.UncostedTokens != label.UncostedTokens ||
		okr.TaskCount != label.TaskCount ||
		okr.UnpricedTaskCount != label.UnpricedTaskCount {
		t.Fatalf("Agent OKR spend = %+v, label usage = %+v", okr, label)
	}
	if okr.TotalTokens != 5_000_123 || okr.TotalCostUSDTicks != 40_000_000_777 ||
		okr.UncostedTokens != 123 || okr.TaskCount != 1 || okr.UnpricedTaskCount != 1 {
		t.Fatalf("unexpected priced OKR spend: %+v", okr)
	}
}
