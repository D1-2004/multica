package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

func TestInternalConnectorManagementRejectsTaskTokenAndURLChange(t *testing.T) {
	t.Setenv("MULTICA_INTERNAL_MCP_ALLOWED_HOST_SUFFIXES", "example.test")
	provider := featureflag.NewStaticProvider()
	provider.Set("internal_mcp_connectors", featureflag.Rule{Default: true})
	router, _ := NewRouterWithOptions(testPool, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil,
		RouterOptions{FeatureFlags: featureflag.NewService(provider)})
	server := httptest.NewServer(router)
	defer server.Close()

	var agentID, runtimeID string
	if err := testPool.QueryRow(t.Context(), `SELECT a.id::text, a.runtime_id::text FROM agent a WHERE a.workspace_id=$1::uuid LIMIT 1`, testWorkspaceID).Scan(&agentID, &runtimeID); err != nil {
		t.Fatal(err)
	}
	var taskID string
	if err := testPool.QueryRow(t.Context(), `INSERT INTO agent_task_queue (agent_id,runtime_id,status,priority) VALUES ($1::uuid,$2::uuid,'running',0) RETURNING id::text`, agentID, runtimeID).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id=$1::uuid`, taskID)
	})
	taskToken := "mat_" + uuid.NewString()
	if _, err := testPool.Exec(t.Context(), `INSERT INTO task_token (token_hash,task_id,agent_id,workspace_id,user_id,expires_at)
		VALUES ($1,$2::uuid,$3::uuid,$4::uuid,$5::uuid,now()+interval '1 hour')`, auth.HashToken(taskToken), taskID, agentID, testWorkspaceID, testUserID); err != nil {
		t.Fatal(err)
	}

	base := "/api/workspaces/" + testWorkspaceID + "/internal-connectors"
	input := map[string]any{"name": "Test", "upstream_url": "https://safe.example.test/mcp", "allowed_tools": []string{"read"}, "agent_ids": []string{}, "enabled": false}
	call := func(method, path, token string, body any) (int, map[string]any) {
		t.Helper()
		encoded, _ := json.Marshal(body)
		req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var result map[string]any
		_ = json.NewDecoder(res.Body).Decode(&result)
		return res.StatusCode, result
	}

	status, created := call(http.MethodPost, base, testToken, input)
	if status != http.StatusOK {
		t.Fatalf("human create: status=%d body=%v", status, created)
	}
	connectorID, _ := created["id"].(string)
	if connectorID == "" {
		t.Fatalf("missing connector ID: %v", created)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM internal_connector WHERE id=$1::uuid`, connectorID)
	})

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, base, nil},
		{http.MethodPost, base, input},
		{http.MethodPatch, base + "/" + connectorID, input},
	} {
		status, result := call(tc.method, tc.path, taskToken, tc.body)
		if status != http.StatusForbidden {
			t.Errorf("task token %s %s: status=%d body=%v", tc.method, tc.path, status, result)
		}
	}

	changed := map[string]any{"name": "Test", "upstream_url": "https://attacker.example.test/collect", "allowed_tools": []string{"read"}, "agent_ids": []string{}, "enabled": false}
	status, result := call(http.MethodPatch, base+"/"+connectorID, testToken, changed)
	if status != http.StatusBadRequest {
		t.Fatalf("changed URL: status=%d body=%v", status, result)
	}
	var storedURL string
	if err := testPool.QueryRow(t.Context(), `SELECT upstream_url FROM internal_connector WHERE id=$1::uuid`, connectorID).Scan(&storedURL); err != nil {
		t.Fatal(err)
	}
	if storedURL != "https://safe.example.test/mcp" {
		t.Fatalf("URL changed despite rejection: %s", storedURL)
	}
	status, result = call(http.MethodPatch, base+"/"+connectorID, testToken, input)
	if status != http.StatusOK {
		t.Fatalf("same-URL update: status=%d body=%v", status, result)
	}
}
