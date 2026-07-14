package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
)

func TestCreateIssueStoresAgentIdentityContextOnlyOnTask(t *testing.T) {
	ctx := context.Background()
	runtimeID := handlerTestRuntimeID(t)

	var previousMetadata []byte
	if err := testPool.QueryRow(ctx,
		`SELECT metadata FROM agent_runtime WHERE id = $1`, runtimeID,
	).Scan(&previousMetadata); err != nil {
		t.Fatalf("load runtime metadata: %v", err)
	}
	metadata, _ := json.Marshal(map[string]any{
		"kind":        service.FCE2BMetadataKind,
		"cli_version": "0.2.21",
	})
	if _, err := testPool.Exec(ctx,
		`UPDATE agent_runtime SET metadata = $1::jsonb WHERE id = $2`, metadata, runtimeID,
	); err != nil {
		t.Fatalf("set FC/E2B metadata: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`UPDATE agent_runtime SET metadata = $1::jsonb WHERE id = $2`, previousMetadata, runtimeID)
	})

	var agentID string
	if err := testPool.QueryRow(ctx,
		`SELECT id FROM agent WHERE workspace_id = $1 AND runtime_id = $2 ORDER BY created_at ASC LIMIT 1`,
		testWorkspaceID, runtimeID,
	).Scan(&agentID); err != nil {
		t.Fatalf("load agent: %v", err)
	}

	const contextToken = "context-token-secret"
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":                        "Agent Identity task context test",
		"assignee_type":                "agent",
		"assignee_id":                  agentID,
		"agent_identity_context_token": contextToken,
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), contextToken) {
		t.Fatal("issue response leaked ContextToken")
	}

	var created IssueResponse
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode issue response: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, created.ID)
	})

	var taskContext []byte
	if err := testPool.QueryRow(ctx, `
		SELECT context
		FROM agent_task_queue
		WHERE issue_id = $1 AND agent_id = $2
		ORDER BY created_at DESC
		LIMIT 1
	`, created.ID, agentID).Scan(&taskContext); err != nil {
		t.Fatalf("load task context: %v", err)
	}
	var stored map[string]any
	if err := json.Unmarshal(taskContext, &stored); err != nil {
		t.Fatalf("decode task context: %v", err)
	}
	if stored["agent_identity_context_token"] != contextToken {
		t.Fatalf("task context token = %#v", stored["agent_identity_context_token"])
	}

	var persistedOnIssue bool
	if err := testPool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM issue
			WHERE id = $1 AND to_jsonb(issue)::text LIKE '%' || $2 || '%'
		)
	`, created.ID, contextToken).Scan(&persistedOnIssue); err != nil {
		t.Fatalf("inspect issue row: %v", err)
	}
	if persistedOnIssue {
		t.Fatal("ContextToken was persisted on the issue row")
	}
}
