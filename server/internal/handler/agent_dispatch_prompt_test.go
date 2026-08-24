package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/featureflag"
)

func readDispatchColumns(t *testing.T, agentID string) (string, bool) {
	t.Helper()
	var prompt string
	var alwaysNew bool
	if err := testPool.QueryRow(context.Background(),
		`SELECT dispatch_prompt, dispatch_always_new_issue FROM agent WHERE id = $1`,
		agentID,
	).Scan(&prompt, &alwaysNew); err != nil {
		t.Fatalf("read dispatch columns: %v", err)
	}
	return prompt, alwaysNew
}

func updateAgentForTest(t *testing.T, agentID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.UpdateAgent(w, withURLParam(newRequestAs(
		testUserID, http.MethodPut, "/api/agents/"+agentID, body,
	), "id", agentID))
	return w
}

// The storage contract for both dispatch fields: a fresh agent starts at the
// managed default, an explicit write persists, an explicit empty string is a
// real clear (not an omission), and an omitted field preserves what is stored.
func TestUpdateAgent_DispatchPromptStorageRoundtrip(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "dispatch-prompt-storage", nil)

	prompt, alwaysNew := readDispatchColumns(t, agentID)
	if prompt != "" || alwaysNew {
		t.Fatalf("new agent defaults = (%q, %v); want (\"\", false)", prompt, alwaysNew)
	}

	// 1. Write both fields.
	authored := "AGENT AUTHORED POLICY\n\n多行内容与 Unicode 都要原样保留。"
	if w := updateAgentForTest(t, agentID, map[string]any{
		"dispatch_prompt":           authored,
		"dispatch_always_new_issue": true,
	}); w.Code != http.StatusOK {
		t.Fatalf("write: got %d: %s", w.Code, w.Body.String())
	}
	prompt, alwaysNew = readDispatchColumns(t, agentID)
	if prompt != authored {
		t.Errorf("stored prompt = %q, want %q", prompt, authored)
	}
	if !alwaysNew {
		t.Errorf("stored dispatch_always_new_issue = false, want true")
	}

	// 2. An unrelated update must not disturb either field.
	if w := updateAgentForTest(t, agentID, map[string]any{
		"description": "unrelated edit",
	}); w.Code != http.StatusOK {
		t.Fatalf("unrelated update: got %d: %s", w.Code, w.Body.String())
	}
	prompt, alwaysNew = readDispatchColumns(t, agentID)
	if prompt != authored || !alwaysNew {
		t.Errorf("omitted fields were disturbed: (%q, %v)", prompt, alwaysNew)
	}

	// 3. An explicit empty string clears the override. The column is
	//    NOT NULL DEFAULT '', so COALESCE keeps this a real write.
	if w := updateAgentForTest(t, agentID, map[string]any{
		"dispatch_prompt":           "",
		"dispatch_always_new_issue": false,
	}); w.Code != http.StatusOK {
		t.Fatalf("clear: got %d: %s", w.Code, w.Body.String())
	}
	prompt, alwaysNew = readDispatchColumns(t, agentID)
	if prompt != "" {
		t.Errorf("after clear, stored prompt = %q, want empty", prompt)
	}
	if alwaysNew {
		t.Errorf("after clear, dispatch_always_new_issue = true, want false")
	}
}

// Both fields must survive the API round-trip, or the settings editor would
// re-open on stale content after a save.
func TestGetAgent_ReturnsDispatchFields(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "dispatch-prompt-response", nil)
	if w := updateAgentForTest(t, agentID, map[string]any{
		"dispatch_prompt":           "AGENT AUTHORED POLICY",
		"dispatch_always_new_issue": true,
	}); w.Code != http.StatusOK {
		t.Fatalf("write: got %d: %s", w.Code, w.Body.String())
	}

	w := httptest.NewRecorder()
	testHandler.GetAgent(w, withURLParam(newRequestAs(
		testUserID, http.MethodGet, "/api/agents/"+agentID, nil,
	), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("GetAgent: got %d: %s", w.Code, w.Body.String())
	}
	var response AgentResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.DispatchPrompt != "AGENT AUTHORED POLICY" {
		t.Errorf("response dispatch_prompt = %q", response.DispatchPrompt)
	}
	if !response.DispatchAlwaysNewIssue {
		t.Errorf("response dispatch_always_new_issue = false, want true")
	}
}

func TestUpdateAgent_DispatchPromptRejectsOversizedPrompt(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "dispatch-prompt-too-long", nil)
	// Multi-byte runes: the cap counts runes, not bytes, so this is just over.
	oversized := strings.Repeat("中", maxAgentDispatchPromptLength+1)
	w := updateAgentForTest(t, agentID, map[string]any{"dispatch_prompt": oversized})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized prompt: got %d, want 400: %s", w.Code, w.Body.String())
	}
	if prompt, _ := readDispatchColumns(t, agentID); prompt != "" {
		t.Errorf("rejected prompt was still persisted (%d chars)", len([]rune(prompt)))
	}

	// The boundary itself is accepted.
	atLimit := strings.Repeat("中", maxAgentDispatchPromptLength)
	if w := updateAgentForTest(t, agentID, map[string]any{"dispatch_prompt": atLimit}); w.Code != http.StatusOK {
		t.Fatalf("prompt at the limit: got %d, want 200: %s", w.Code, w.Body.String())
	}
}

// The editor seeds itself from this endpoint, so it must serve the live
// Diamond common section rather than a build-time constant.
func TestGetAgentDispatchPromptDefaultServesManagedCommonPolicy(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "dispatch-prompt-default", nil)

	provider := featureflag.NewDiamondProvider()
	if _, _, err := provider.ApplyJSON([]byte(`{"common":{"prompt":"MANAGED COMMON POLICY"}}`)); err != nil {
		t.Fatalf("seed Diamond prompt: %v", err)
	}
	previous := testHandler.FeatureFlags
	testHandler.FeatureFlags = featureflag.NewService(provider)
	t.Cleanup(func() { testHandler.FeatureFlags = previous })

	w := httptest.NewRecorder()
	testHandler.GetAgentDispatchPromptDefault(w, withURLParam(newRequestAs(
		testUserID, http.MethodGet, "/api/agents/"+agentID+"/dispatch-prompt-default", nil,
	), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("GetAgentDispatchPromptDefault: got %d: %s", w.Code, w.Body.String())
	}
	var response AgentDispatchPromptDefaultResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.Prompt != "MANAGED COMMON POLICY" {
		t.Errorf("default prompt = %q, want the live Diamond common section", response.Prompt)
	}
}
