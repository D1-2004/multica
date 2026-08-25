package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	if response.OKRs[0].KeyResults[0].Label != "KR: 平均响应<2h" {
		t.Errorf("key result label = %q", response.OKRs[0].KeyResults[0].Label)
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

// The prompt must name the exact label strings and the exact command; an agent
// told only "tag the issue" invents labels and the catalog fragments.
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
		"O: 提升交付效率",
		"KR: 平均响应<2h",
		"multica issue label add",
		"do not invent",
	} {
		if !strings.Contains(instructions, want) {
			t.Errorf("OKR instructions missing %q:\n%s", want, instructions)
		}
	}
}

func TestBuildAgentOKRInstructionsIsEmptyWithoutOKRs(t *testing.T) {
	t.Parallel()
	if got := buildAgentOKRInstructions(nil); got != "" {
		t.Fatalf("expected no OKR section, got %q", got)
	}
}
