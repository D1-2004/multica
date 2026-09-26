package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	internalflags "github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

func TestWorkspaceMCPRouterDispatchAndTokenLifecycle(t *testing.T) {
	t.Setenv("MULTICA_PUBLIC_URL", "http://localhost")
	provider := featureflag.NewStaticProvider()
	provider.Set(internalflags.WorkspaceMCPEndpoint, featureflag.Rule{Default: true})
	provider.Set(internalflags.WorkspaceMCPReplaceAgentLinks, featureflag.Rule{Default: true})
	router, _ := NewRouterWithOptions(testPool, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil,
		RouterOptions{FeatureFlags: featureflag.NewService(provider)})
	server := httptest.NewServer(router)
	defer server.Close()
	call := func(path, bearer string, body any) (int, map[string]any) {
		t.Helper()
		encoded, _ := json.Marshal(body)
		req, err := http.NewRequest(http.MethodPost, server.URL+path, bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var data map[string]any
		_ = json.NewDecoder(res.Body).Decode(&data)
		return res.StatusCode, data
	}
	status, created := call("/api/workspaces/"+testWorkspaceID+"/mcp-tokens/", testToken,
		map[string]any{"name": "integration", "service_name": "MCP integration client", "service_role": "admin", "scopes": []string{"read", "write", "manage"}})
	if status != http.StatusCreated {
		t.Fatalf("create token status=%d body=%v", status, created)
	}
	secret, _ := created["token"].(string)
	tokenID, _ := created["id"].(string)
	subjectID, _ := created["subject_user_id"].(string)
	if secret == "" || tokenID == "" {
		t.Fatalf("token response missing fields: %v", created)
	}
	status, _ = call("/api/workspaces/"+testWorkspaceID+"/mcp-tokens/", testToken,
		map[string]any{"name": "impersonation", "subject_user_id": subjectID, "scopes": []string{"read"}})
	if status != http.StatusForbidden {
		t.Fatalf("issuing as another subject status=%d", status)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(t.Context(), `DELETE FROM workspace_mcp_audit WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(t.Context(), `DELETE FROM workspace_mcp_token WHERE workspace_id=$1`, testWorkspaceID)
		_, _ = testPool.Exec(t.Context(), `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`, testWorkspaceID, subjectID)
		_, _ = testPool.Exec(t.Context(), `DELETE FROM "user" WHERE id=$1`, subjectID)
	})
	endpoint := "/api/mcp/workspaces/" + testWorkspaceID + "/connect/" + secret
	if created["url"] != "http://localhost"+endpoint {
		t.Fatal("missing workspace link")
	}
	status, _ = call("/api/mcp/workspaces/"+testWorkspaceID, secret,
		map[string]any{"jsonrpc": "2.0", "id": 0, "method": "tools/list"})
	if status != http.StatusOK {
		t.Fatalf("existing Bearer endpoint status=%d", status)
	}
	mcp := func(id int, method string, params any) (int, map[string]any) {
		return call(endpoint, "", map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	}
	status, listed := mcp(1, "tools/list", nil)
	if status != http.StatusOK {
		t.Fatalf("tools/list status=%d body=%v", status, listed)
	}
	result, _ := listed["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	if len(tools) < 40 {
		t.Fatalf("too few tools: %d", len(tools))
	}
	toolCall := func(id int, name string, arguments any) any {
		t.Helper()
		status, data := mcp(id, "tools/call", map[string]any{"name": name, "arguments": arguments})
		if status != http.StatusOK {
			t.Fatalf("%s status=%d body=%v", name, status, data)
		}
		result, _ := data["result"].(map[string]any)
		if result["isError"] == true {
			t.Fatalf("%s tool error: %v", name, result)
		}
		structured, _ := result["structuredContent"].(map[string]any)
		return structured["data"]
	}
	issue, _ := toolCall(2, "issue_create", map[string]any{"payload": map[string]any{"title": "Workspace MCP integration"}}).(map[string]any)
	issueID, _ := issue["id"].(string)
	if issueID == "" {
		t.Fatalf("issue create returned no id: %v", issue)
	}
	t.Cleanup(func() { _, _ = testPool.Exec(t.Context(), `DELETE FROM issue WHERE id=$1`, issueID) })
	got, _ := toolCall(3, "issue_get", map[string]any{"id": issueID}).(map[string]any)
	if got["id"] != issueID {
		t.Fatalf("issue get mismatch: %v", got)
	}
	var runtimeID string
	if err := testPool.QueryRow(t.Context(), `SELECT id::text FROM agent_runtime WHERE workspace_id=$1 LIMIT 1`, testWorkspaceID).Scan(&runtimeID); err != nil {
		t.Fatal(err)
	}
	agent, _ := toolCall(4, "agent_create", map[string]any{"payload": map[string]any{"name": "MCP test agent", "runtime_id": runtimeID}}).(map[string]any)
	agentID, _ := agent["id"].(string)
	if agentID == "" {
		t.Fatalf("agent create returned no id: %v", agent)
	}
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/api/agents/"+agentID+"/a2a/", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Endpoint struct {
			MCPURL          string `json:"mcp_url"`
			WorkspaceMCPURL string `json:"workspace_mcp_url"`
		} `json:"endpoint"`
	}
	err = json.NewDecoder(res.Body).Decode(&config)
	res.Body.Close()
	if err != nil || res.StatusCode != http.StatusOK || config.Endpoint.MCPURL == "" || config.Endpoint.WorkspaceMCPURL != "" {
		t.Fatalf("agent MCP entry was replaced: status=%d config=%+v err=%v", res.StatusCode, config, err)
	}
	toolCall(5, "issue_assign", map[string]any{"id": issueID, "payload": map[string]any{"assignee_type": "agent", "assignee_id": agentID}})
	runs, _ := toolCall(6, "issue_runs", map[string]any{"id": issueID}).([]any)
	if len(runs) == 0 {
		t.Fatal("issue runs returned no run")
	}
	runtimes, _ := toolCall(7, "runtime_list", map[string]any{}).([]any)
	if len(runtimes) == 0 {
		t.Fatal("runtime list returned no runtime")
	}
	_, cross := call("/api/mcp/workspaces/00000000-0000-0000-0000-000000000000/connect/"+secret, "",
		map[string]any{"jsonrpc": "2.0", "id": 8, "method": "tools/list"})
	if cross["error"] != "workspace mismatch" {
		t.Fatalf("cross workspace response=%v", cross)
	}
	status, _ = call("/api/workspaces/"+testWorkspaceID+"/mcp-tokens/"+tokenID+"/revoke", testToken, map[string]any{})
	if status != http.StatusNoContent {
		t.Fatalf("revoke status=%d", status)
	}
	status, _ = mcp(9, "tools/list", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("revoked token status=%d", status)
	}
}
