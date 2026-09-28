package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

func TestInternalConnectorManagementRejectsTaskTokenAndURLChange(t *testing.T) {
	t.Setenv("MULTICA_INTERNAL_MCP_ALLOWED_HOST_SUFFIXES", "safe.example.test")
	t.Setenv("MULTICA_INTERNAL_MCP_SECRET_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("k"), 32)))
	provider := featureflag.NewStaticProvider()
	provider.Set("internal_mcp_connectors", featureflag.Rule{Default: true})
	router, handler := NewRouterWithOptions(testPool, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil,
		RouterOptions{FeatureFlags: featureflag.NewService(provider)})
	handler.InternalConnectorClient = &http.Client{Transport: connectorTestTransport{t}}
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
	status, result := call(http.MethodPut, base+"/"+connectorID+"/credential", testToken, map[string]string{"bearer_token": "workspace-secret"})
	if status != http.StatusOK || result["credential_ready"] != true {
		t.Fatalf("human credential write: status=%d body=%v", status, result)
	}
	var ciphertext []byte
	if err := testPool.QueryRow(t.Context(), `SELECT credential_ciphertext FROM internal_connector WHERE id=$1::uuid`, connectorID).Scan(&ciphertext); err != nil || len(ciphertext) == 0 || bytes.Contains(ciphertext, []byte("workspace-secret")) {
		t.Fatalf("credential not sealed: len=%d err=%v", len(ciphertext), err)
	}
	status, result = call(http.MethodPost, base+"/"+connectorID+"/test", testToken, nil)
	if status != http.StatusOK || result["reachable"] != true || strings.Contains(fmt.Sprint(result), "workspace-secret") {
		t.Fatalf("connectivity test failed or leaked credential: status=%d body=%v", status, result)
	}

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, base, nil},
		{http.MethodPost, base, input},
		{http.MethodPatch, base + "/" + connectorID, input},
		{http.MethodPut, base + "/" + connectorID + "/credential", map[string]string{"bearer_token": "attacker"}},
		{http.MethodPost, base + "/" + connectorID + "/test", nil},
	} {
		status, result := call(tc.method, tc.path, taskToken, tc.body)
		if status != http.StatusForbidden {
			t.Errorf("task token %s %s: status=%d body=%v", tc.method, tc.path, status, result)
		}
	}

	changed := map[string]any{"name": "Test", "upstream_url": "https://attacker.example.test/collect", "allowed_tools": []string{"read"}, "agent_ids": []string{}, "enabled": false}
	status, result = call(http.MethodPatch, base+"/"+connectorID, testToken, changed)
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
	active := map[string]any{"name": "Test", "upstream_url": "https://safe.example.test/mcp", "allowed_tools": []string{"read"}, "agent_ids": []string{agentID}, "enabled": true}
	status, result = call(http.MethodPatch, base+"/"+connectorID, testToken, active)
	if status != http.StatusOK {
		t.Fatalf("could not enable authorized connector: status=%d body=%v", status, result)
	}
	status, result = call(http.MethodPost, "/api/internal-connectors/"+connectorID+"/mcp", taskToken, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	if status != http.StatusOK || result["result"] == nil {
		t.Fatalf("authorized Agent could not initialize connector: status=%d body=%v", status, result)
	}
	otherWS := uuid.NewString()
	if _, err := testPool.Exec(t.Context(), `INSERT INTO workspace (id,name,slug,description) VALUES ($1::uuid,'Other','connector-boundary-' || $2,'')`, otherWS, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	var otherRuntime, otherAgent, otherTask string
	if err := testPool.QueryRow(t.Context(), `INSERT INTO agent_runtime (workspace_id,name,runtime_mode,provider,status,device_info,metadata,last_seen_at) VALUES ($1::uuid,'Other','cloud','integration_test_runtime','online','Other runtime','{}'::jsonb,now()) RETURNING id::text`, otherWS).Scan(&otherRuntime); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(t.Context(), `INSERT INTO agent (workspace_id,name,description,runtime_mode,runtime_config,runtime_id,visibility,max_concurrent_tasks,owner_id) VALUES ($1::uuid,'Other Agent','','cloud','{}'::jsonb,$2::uuid,'workspace',1,$3::uuid) RETURNING id::text`, otherWS, otherRuntime, testUserID).Scan(&otherAgent); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(t.Context(), `INSERT INTO agent_task_queue (agent_id,runtime_id,status,priority) VALUES ($1::uuid,$2::uuid,'running',0) RETURNING id::text`, otherAgent, otherRuntime).Scan(&otherTask); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testPool.Exec(ctx, `DELETE FROM task_token WHERE task_id=$1::uuid`, otherTask)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id=$1::uuid`, otherTask)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent WHERE id=$1::uuid`, otherAgent)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id=$1::uuid`, otherRuntime)
		_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE id=$1::uuid`, otherWS)
	})
	otherToken := "mat_" + uuid.NewString()
	if _, err := testPool.Exec(t.Context(), `INSERT INTO task_token (token_hash,task_id,agent_id,workspace_id,user_id,expires_at) VALUES ($1,$2::uuid,$3::uuid,$4::uuid,$5::uuid,now()+interval '1 hour')`, auth.HashToken(otherToken), otherTask, otherAgent, otherWS, testUserID); err != nil {
		t.Fatal(err)
	}
	status, result = call(http.MethodPatch, base+"/"+connectorID, testToken, map[string]any{"name": "Test", "upstream_url": "https://safe.example.test/mcp", "allowed_tools": []string{"read"}, "agent_ids": []string{otherAgent}, "enabled": true})
	if status != http.StatusBadRequest {
		t.Fatalf("cross-workspace Agent grant accepted: %d %v", status, result)
	}
	status, result = call(http.MethodPost, "/api/internal-connectors/"+connectorID+"/mcp", otherToken, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if status != http.StatusForbidden {
		t.Fatalf("other workspace task reached connector: %d %v", status, result)
	}
}

type connectorTestTransport struct{ t *testing.T }

func (transport connectorTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != "safe.example.test" || req.Header.Get("Authorization") != "Bearer workspace-secret" || req.Header.Get("X-Task-ID") != "" {
		transport.t.Errorf("connector test escaped fixed target or credential boundary: host=%s", req.URL.Host)
	}
	var rpc struct {
		Method string `json:"method"`
	}
	if err := json.NewDecoder(req.Body).Decode(&rpc); err != nil || rpc.Method != "tools/list" {
		transport.t.Errorf("connectivity test invoked an unexpected method: %q %v", rpc.Method, err)
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read","inputSchema":{"type":"object"}}]}}`))}, nil
}
