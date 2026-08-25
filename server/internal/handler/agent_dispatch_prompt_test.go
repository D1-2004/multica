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

func readDispatchColumns(t *testing.T, agentID string) (map[string]string, bool) {
	t.Helper()
	var overrides []byte
	var alwaysNew bool
	if err := testPool.QueryRow(context.Background(),
		`SELECT dispatch_prompt_overrides, dispatch_always_new_issue FROM agent WHERE id = $1`,
		agentID,
	).Scan(&overrides, &alwaysNew); err != nil {
		t.Fatalf("read dispatch columns: %v", err)
	}
	return parseDispatchPromptOverrides(overrides), alwaysNew
}

func updateAgentForTest(t *testing.T, agentID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.UpdateAgent(w, withURLParam(newRequestAs(
		testUserID, http.MethodPut, "/api/agents/"+agentID, body,
	), "id", agentID))
	return w
}

// The storage contract: a fresh agent starts on the managed text, an explicit
// write persists per segment, an omitted field preserves what is stored, and
// dropping a key restores the managed text for that segment alone.
func TestUpdateAgent_DispatchPromptOverridesStorageRoundtrip(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "dispatch-overrides-storage", nil)

	overrides, alwaysNew := readDispatchColumns(t, agentID)
	if len(overrides) != 0 || alwaysNew {
		t.Fatalf("new agent defaults = (%v, %v); want (empty, false)", overrides, alwaysNew)
	}

	authored := "AGENT AUTHORED POLICY\n\n多行内容与 Unicode 都要原样保留。"
	if w := updateAgentForTest(t, agentID, map[string]any{
		"dispatch_prompt_overrides": map[string]string{
			DispatchSegmentPolicy:          authored,
			DispatchSegmentReplyFormatting: "AGENT REPLY RULES",
		},
		"dispatch_always_new_issue": true,
	}); w.Code != http.StatusOK {
		t.Fatalf("write: got %d: %s", w.Code, w.Body.String())
	}
	overrides, alwaysNew = readDispatchColumns(t, agentID)
	if overrides[DispatchSegmentPolicy] != authored {
		t.Errorf("stored policy = %q, want %q", overrides[DispatchSegmentPolicy], authored)
	}
	if overrides[DispatchSegmentReplyFormatting] != "AGENT REPLY RULES" {
		t.Errorf("stored reply formatting = %q", overrides[DispatchSegmentReplyFormatting])
	}
	if !alwaysNew {
		t.Errorf("stored dispatch_always_new_issue = false, want true")
	}

	// An unrelated update must not disturb the overrides.
	if w := updateAgentForTest(t, agentID, map[string]any{
		"description": "unrelated edit",
	}); w.Code != http.StatusOK {
		t.Fatalf("unrelated update: got %d: %s", w.Code, w.Body.String())
	}
	overrides, _ = readDispatchColumns(t, agentID)
	if len(overrides) != 2 {
		t.Errorf("omitted field disturbed the overrides: %v", overrides)
	}

	// Dropping one key restores the managed text for that segment only.
	if w := updateAgentForTest(t, agentID, map[string]any{
		"dispatch_prompt_overrides": map[string]string{DispatchSegmentPolicy: authored},
	}); w.Code != http.StatusOK {
		t.Fatalf("partial clear: got %d: %s", w.Code, w.Body.String())
	}
	overrides, _ = readDispatchColumns(t, agentID)
	if _, present := overrides[DispatchSegmentReplyFormatting]; present {
		t.Errorf("dropped key survived: %v", overrides)
	}
	if overrides[DispatchSegmentPolicy] != authored {
		t.Errorf("partial clear also dropped policy: %v", overrides)
	}

	// An empty map clears everything.
	if w := updateAgentForTest(t, agentID, map[string]any{
		"dispatch_prompt_overrides": map[string]string{},
		"dispatch_always_new_issue": false,
	}); w.Code != http.StatusOK {
		t.Fatalf("clear: got %d: %s", w.Code, w.Body.String())
	}
	overrides, alwaysNew = readDispatchColumns(t, agentID)
	if len(overrides) != 0 || alwaysNew {
		t.Errorf("after clear = (%v, %v); want (empty, false)", overrides, alwaysNew)
	}
}

// A blank override is normalized away, so "restore managed" does not depend on
// whether the client omitted the key or sent an empty string.
func TestUpdateAgent_BlankSegmentOverrideIsTreatedAsRestore(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "dispatch-overrides-blank", nil)
	if w := updateAgentForTest(t, agentID, map[string]any{
		"dispatch_prompt_overrides": map[string]string{DispatchSegmentPolicy: "   \n\t "},
	}); w.Code != http.StatusOK {
		t.Fatalf("blank write: got %d: %s", w.Code, w.Body.String())
	}
	if overrides, _ := readDispatchColumns(t, agentID); len(overrides) != 0 {
		t.Errorf("blank override was stored: %v", overrides)
	}
}

// A typo that stores cleanly but never takes effect is the worst outcome for
// someone editing a prompt they cannot otherwise observe.
func TestUpdateAgent_RejectsUnknownAndUncustomizableSegments(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "dispatch-overrides-unknown", nil)

	for name, key := range map[string]string{
		"unknown segment":        "polcy",
		"uncustomizable segment": DispatchSegmentContext,
	} {
		w := updateAgentForTest(t, agentID, map[string]any{
			"dispatch_prompt_overrides": map[string]string{key: "text"},
		})
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400: %s", name, w.Code, w.Body.String())
		}
		if overrides, _ := readDispatchColumns(t, agentID); len(overrides) != 0 {
			t.Errorf("%s: rejected write was persisted: %v", name, overrides)
		}
	}
}

func TestGetAgent_ReturnsDispatchFields(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "dispatch-overrides-response", nil)
	if w := updateAgentForTest(t, agentID, map[string]any{
		"dispatch_prompt_overrides": map[string]string{DispatchSegmentPolicy: "AGENT AUTHORED POLICY"},
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
	if response.DispatchPromptOverrides[DispatchSegmentPolicy] != "AGENT AUTHORED POLICY" {
		t.Errorf("response overrides = %v", response.DispatchPromptOverrides)
	}
	if !response.DispatchAlwaysNewIssue {
		t.Errorf("response dispatch_always_new_issue = false, want true")
	}
}

func TestUpdateAgent_DispatchPromptRejectsOversizedSegment(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "dispatch-overrides-too-long", nil)
	// Multi-byte runes: the cap counts runes, not bytes, so this is just over.
	oversized := strings.Repeat("中", maxAgentDispatchPromptLength+1)
	w := updateAgentForTest(t, agentID, map[string]any{
		"dispatch_prompt_overrides": map[string]string{DispatchSegmentPolicy: oversized},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized segment: got %d, want 400: %s", w.Code, w.Body.String())
	}
	if overrides, _ := readDispatchColumns(t, agentID); len(overrides) != 0 {
		t.Errorf("rejected segment was persisted")
	}

	atLimit := strings.Repeat("中", maxAgentDispatchPromptLength)
	if w := updateAgentForTest(t, agentID, map[string]any{
		"dispatch_prompt_overrides": map[string]string{DispatchSegmentPolicy: atLimit},
	}); w.Code != http.StatusOK {
		t.Fatalf("segment at the limit: got %d, want 200: %s", w.Code, w.Body.String())
	}
}

// The preview must be produced by the same composer the claim path uses, or it
// becomes a second implementation that drifts.
func TestGetAgentDispatchPromptPreviewMatchesTheClaimComposition(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "dispatch-preview", nil)

	provider := featureflag.NewDiamondProvider()
	if _, _, err := provider.ApplyJSON([]byte(`{
	  "common":{"prompt":"MANAGED COMMON POLICY"},
	  "auto":{"prompt":"MANAGED AUTO POLICY"}
	}`)); err != nil {
		t.Fatalf("seed Diamond prompts: %v", err)
	}
	previous := testHandler.FeatureFlags
	testHandler.FeatureFlags = featureflag.NewService(provider)
	t.Cleanup(func() { testHandler.FeatureFlags = previous })

	w := httptest.NewRecorder()
	testHandler.GetAgentDispatchPromptPreview(w, withURLParam(newRequestAs(
		testUserID, http.MethodGet, "/api/agents/"+agentID+"/dispatch-prompt-preview?surface=auto", nil,
	), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("preview: got %d: %s", w.Code, w.Body.String())
	}
	var response AgentDispatchPromptPreviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if response.Surface != "auto" {
		t.Errorf("surface = %q", response.Surface)
	}
	byID := map[string]DispatchPromptSegment{}
	for _, segment := range response.Segments {
		byID[segment.ID] = segment
	}
	policy := byID[DispatchSegmentPolicy]
	if policy.ManagedText != "MANAGED COMMON POLICY\n\nMANAGED AUTO POLICY" {
		t.Errorf("policy managed text = %q", policy.ManagedText)
	}
	if policy.Overridden || !policy.Included {
		t.Errorf("policy segment = %+v", policy)
	}
	// The Router context has no content until a real dispatch resolves it, so
	// the preview must mark it excluded rather than showing a blank section.
	if ctx := byID[DispatchSegmentContext]; ctx.Customizable || ctx.Included {
		t.Errorf("context segment = %+v; want non-customizable and excluded", ctx)
	}
	if !strings.Contains(response.Instruction, "MANAGED AUTO POLICY") {
		t.Errorf("assembled instruction = %q", response.Instruction)
	}
	if len(response.RuntimeSections) == 0 {
		t.Errorf("preview omitted the runtime-composed section index")
	}
}

// Switching mode must change only the mode half of the policy segment.
func TestGetAgentDispatchPromptPreviewRendersEachSurface(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "dispatch-preview-surfaces", nil)

	provider := featureflag.NewDiamondProvider()
	if _, _, err := provider.ApplyJSON([]byte(`{
	  "common":{"prompt":"COMMON"},
	  "issue":{"prompt":"ISSUE POLICY"},
	  "chat":{"prompt":"CHAT POLICY"},
	  "auto":{"prompt":"AUTO POLICY"}
	}`)); err != nil {
		t.Fatalf("seed Diamond prompts: %v", err)
	}
	previous := testHandler.FeatureFlags
	testHandler.FeatureFlags = featureflag.NewService(provider)
	t.Cleanup(func() { testHandler.FeatureFlags = previous })

	for surface, want := range map[string]string{
		"issue": "ISSUE POLICY",
		"chat":  "CHAT POLICY",
		"auto":  "AUTO POLICY",
	} {
		w := httptest.NewRecorder()
		testHandler.GetAgentDispatchPromptPreview(w, withURLParam(newRequestAs(
			testUserID, http.MethodGet,
			"/api/agents/"+agentID+"/dispatch-prompt-preview?surface="+surface, nil,
		), "id", agentID))
		if w.Code != http.StatusOK {
			t.Fatalf("preview %s: got %d: %s", surface, w.Code, w.Body.String())
		}
		var response AgentDispatchPromptPreviewResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode %s: %v", surface, err)
		}
		if !strings.Contains(response.Instruction, want) {
			t.Errorf("surface %s instruction = %q, want it to contain %q", surface, response.Instruction, want)
		}
	}

	w := httptest.NewRecorder()
	testHandler.GetAgentDispatchPromptPreview(w, withURLParam(newRequestAs(
		testUserID, http.MethodGet, "/api/agents/"+agentID+"/dispatch-prompt-preview?surface=nope", nil,
	), "id", agentID))
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid surface: got %d, want 400", w.Code)
	}
}

// An override must show up in the preview as the effective text while the
// managed text stays visible, so the owner can compare before committing.
func TestGetAgentDispatchPromptPreviewShowsOverrideAgainstManaged(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "dispatch-preview-override", nil)

	provider := featureflag.NewDiamondProvider()
	if _, _, err := provider.ApplyJSON([]byte(`{"common":{"prompt":"MANAGED COMMON"}}`)); err != nil {
		t.Fatalf("seed Diamond prompt: %v", err)
	}
	previous := testHandler.FeatureFlags
	testHandler.FeatureFlags = featureflag.NewService(provider)
	t.Cleanup(func() { testHandler.FeatureFlags = previous })

	if w := updateAgentForTest(t, agentID, map[string]any{
		"dispatch_prompt_overrides": map[string]string{DispatchSegmentPolicy: "AGENT POLICY"},
	}); w.Code != http.StatusOK {
		t.Fatalf("write override: got %d: %s", w.Code, w.Body.String())
	}

	w := httptest.NewRecorder()
	testHandler.GetAgentDispatchPromptPreview(w, withURLParam(newRequestAs(
		testUserID, http.MethodGet, "/api/agents/"+agentID+"/dispatch-prompt-preview", nil,
	), "id", agentID))
	if w.Code != http.StatusOK {
		t.Fatalf("preview: got %d: %s", w.Code, w.Body.String())
	}
	var response AgentDispatchPromptPreviewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, segment := range response.Segments {
		if segment.ID != DispatchSegmentPolicy {
			continue
		}
		if !segment.Overridden {
			t.Errorf("policy segment not marked overridden")
		}
		if segment.EffectiveText != "AGENT POLICY" {
			t.Errorf("effective = %q, want the authored text", segment.EffectiveText)
		}
		if !strings.Contains(segment.ManagedText, "MANAGED COMMON") {
			t.Errorf("managed text was lost: %q", segment.ManagedText)
		}
	}
	if strings.Contains(response.Instruction, "MANAGED COMMON") {
		t.Errorf("assembled instruction still carries the managed policy: %q", response.Instruction)
	}
}
