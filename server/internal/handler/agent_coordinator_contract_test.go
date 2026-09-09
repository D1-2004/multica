package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/coordinatorcontract"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestAgentCoordinatorContractBoundary(t *testing.T) {
	raw := []byte(`{"version":1,"scope":"产品查证","must_delegate":["专业问答"],"constraints":["只起草"],"clarify_when":[]}`)
	bound, err := bindAgentCoordinatorContract(raw, "job instructions")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bound), "source_instructions_sha256") {
		t.Fatal("Host did not bind instructions")
	}
	copied, err := bindAgentCoordinatorContract(bound, "changed")
	if err != nil {
		t.Fatal(err)
	}
	if _, state := coordinatorcontract.Resolve(copied, "changed"); state != coordinatorcontract.StateStale {
		t.Fatal("copy silently rebound stale contract")
	}
	h := &Handler{}
	for _, test := range []struct{ instructions, state string }{{"job instructions", "loaded"}, {"changed", "stale"}} {
		resp := h.agentToResponse(db.Agent{Instructions: test.instructions, CoordinatorContract: bound})
		if resp.CoordinatorContractState != test.state || resp.CoordinatorContract == nil {
			t.Fatalf("response=%+v", resp)
		}
	}
	if _, err := bindAgentCoordinatorContract([]byte(`{"version":1,"scope":"x","reply":"escape"}`), ""); err == nil {
		t.Fatal("unknown contract field accepted")
	}
	if raw, err := bindAgentCoordinatorContract([]byte("null"), ""); err != nil || raw != nil {
		t.Fatalf("clear=(%s,%v)", raw, err)
	}
}

func TestUpdateAgentCoordinatorContractRoundtrip(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "coordinator-contract-storage", nil)
	authored := map[string]any{"version": 1, "scope": "产品问答", "must_delegate": []string{"查证"}, "constraints": []string{"只起草"}, "clarify_when": []string{}}
	check := func(body map[string]any, want string) {
		t.Helper()
		w := updateAgentForTest(t, agentID, body)
		if w.Code != http.StatusOK {
			t.Fatalf("update: %d %s", w.Code, w.Body.String())
		}
		var response AgentResponse
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.CoordinatorContractState != want {
			t.Fatalf("state=%s, want %s", response.CoordinatorContractState, want)
		}
	}
	check(map[string]any{"instructions": "original", "coordinator_contract": authored}, coordinatorcontract.StateLoaded)
	if _, err := testPool.Exec(context.Background(), "UPDATE agent SET status = 'working' WHERE id = $1", agentID); err != nil {
		t.Fatal(err)
	}
	refreshed, err := testHandler.Queries.RefreshAgentStatusFromTasks(context.Background(), parseUUID(agentID))
	if err != nil {
		t.Fatalf("refresh Agent projection: %v", err)
	}
	if _, state := coordinatorcontract.Resolve(refreshed.CoordinatorContract, "original"); state != coordinatorcontract.StateLoaded {
		t.Fatalf("refresh lost contract: %s", state)
	}
	check(map[string]any{"description": "preserve contract"}, coordinatorcontract.StateLoaded)
	check(map[string]any{"instructions": "changed"}, coordinatorcontract.StateStale)
	check(map[string]any{"coordinator_contract": authored}, coordinatorcontract.StateLoaded)
	check(map[string]any{"coordinator_contract": nil}, coordinatorcontract.StateNotConfigured)
}

func TestUpdateLocalPackageAgentPreservesSourceContract(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	agentID := createHandlerTestAgent(t, "local-package-contract-protection", nil)
	bound, err := bindAgentCoordinatorContract([]byte(`{"version":1,"scope":"Product support","constraints":["Draft only"],"must_delegate":[],"clarify_when":[]}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testHandler.Queries.UpdateAgent(t.Context(), db.UpdateAgentParams{ID: parseUUID(agentID), CoordinatorContract: bound}); err != nil {
		t.Fatal(err)
	}
	if _, err := testHandler.Queries.CreateLocalAgentSource(t.Context(), db.CreateLocalAgentSourceParams{
		AgentID: parseUUID(agentID), WorkspaceID: parseUUID(testWorkspaceID), SyncedCommitSha: "package-hash", CreatedBy: parseUUID(testUserID),
	}); err != nil {
		t.Fatal(err)
	}
	for _, body := range []map[string]any{
		{"instructions": "local override"},
		{"coordinator_contract": nil},
		{"coordinator_contract": map[string]any{"version": 1, "scope": "changed"}},
	} {
		w := updateAgentForTest(t, agentID, body)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "local package source") {
			t.Fatalf("source edit must identify the local package: %d %s", w.Code, w.Body.String())
		}
	}
	if w := updateAgentForTest(t, agentID, map[string]any{"description": "Profile remains editable"}); w.Code != http.StatusOK {
		t.Fatalf("profile edit rejected: %d %s", w.Code, w.Body.String())
	}
	row, err := testHandler.Queries.GetAgent(t.Context(), parseUUID(agentID))
	if err != nil {
		t.Fatal(err)
	}
	actual, state := coordinatorcontract.Resolve(row.CoordinatorContract, row.Instructions)
	expected, _ := coordinatorcontract.Parse(bound)
	if state != coordinatorcontract.StateLoaded || coordinatorcontract.Hash(actual) != coordinatorcontract.Hash(expected) {
		t.Fatalf("source contract changed: state=%s", state)
	}
}
