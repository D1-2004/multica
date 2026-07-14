package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClaimTaskByRuntime_FCE2BDoesNotForwardAgentModel(t *testing.T) {
	assertClaimedAgentModel(t, `{"kind":"fc-e2b"}`, "")
}

func TestClaimTaskByRuntime_NonFCE2BForwardsAgentModel(t *testing.T) {
	assertClaimedAgentModel(t, `{}`, "qwen3.7-plus")
}

func assertClaimedAgentModel(t *testing.T, runtimeMetadata, wantModel string) {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider,
			status, device_info, metadata, last_seen_at, visibility, owner_id
		)
		VALUES ($1, NULL, 'claim model fixture', 'cloud', 'hermes',
			'online', 'claim model fixture', $2::jsonb, now(), 'private', $3)
		RETURNING id
	`, testWorkspaceID, runtimeMetadata, testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("setup: create runtime: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID) })

	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "Claim model agent")
	if _, err := testPool.Exec(ctx, `UPDATE agent SET model = 'qwen3.7-plus' WHERE id = $1`, agentID); err != nil {
		t.Fatalf("setup: save agent model: %v", err)
	}

	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority)
		VALUES ($1, $2, $3, 'queued', 0)
		RETURNING id
	`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("setup: create queued task: %v", err)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })

	w := httptest.NewRecorder()
	req := newDaemonTokenRequest(
		http.MethodPost,
		"/api/daemon/runtimes/"+runtimeID+"/tasks/claim",
		nil,
		testWorkspaceID,
		"claim-model-fixture",
	)
	req = withURLParam(req, "runtimeId", runtimeID)
	testHandler.ClaimTaskByRuntime(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ClaimTaskByRuntime: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Task *struct {
			ID    string         `json:"id"`
			Agent *TaskAgentData `json:"agent"`
		} `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode claim response: %v", err)
	}
	if resp.Task == nil || resp.Task.Agent == nil {
		t.Fatalf("expected claimed task with agent: %s", w.Body.String())
	}
	if resp.Task.ID != taskID {
		t.Fatalf("claimed task = %s, want %s", resp.Task.ID, taskID)
	}
	if resp.Task.Agent.Model != wantModel {
		t.Fatalf("claimed agent model = %q, want %q", resp.Task.Agent.Model, wantModel)
	}
}
