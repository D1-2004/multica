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
	handler.InternalConnectorClient = &http.Client{Transport: connectorTestTransport{t: t}}
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

	invalidTarget := map[string]any{"name": "Test", "upstream_url": "https://other.example.test/mcp", "allowed_tools": []string{"read"}, "agent_ids": []string{}, "enabled": false}
	status, rejected := call(http.MethodPost, base, testToken, invalidTarget)
	if status != http.StatusBadRequest || rejected["error"] != "upstream host is not in the deployment allowlist" {
		t.Fatalf("admin did not receive actionable validation reason: status=%d body=%v", status, rejected)
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
	if status != http.StatusOK || result["reachable"] != true || result["ready"] != true || strings.Contains(fmt.Sprint(result), "workspace-secret") {
		t.Fatalf("connectivity test failed or leaked credential: status=%d body=%v", status, result)
	}
	handler.InternalConnectorClient = &http.Client{Transport: connectorTestTransport{t: t, empty: true}}
	status, result = call(http.MethodPost, base+"/"+connectorID+"/test", testToken, nil)
	if status != http.StatusOK || result["reachable"] != true || result["ready"] != true || strings.Contains(fmt.Sprint(result["missing_tools"]), "read") {
		t.Fatalf("empty upstream tool list was rejected: status=%d body=%v", status, result)
	}
	handler.InternalConnectorClient = &http.Client{Transport: connectorTestTransport{t: t}}

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

func TestInternalConnectorImportsHostedAgentCapabilityWithoutPersistingItsLink(t *testing.T) {
	t.Setenv("MULTICA_INTERNAL_MCP_ALLOWED_HOST_SUFFIXES", "safe.example.test")
	t.Setenv("MULTICA_INTERNAL_MCP_SECRET_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("k"), 32)))
	t.Setenv("MULTICA_PUBLIC_URL", "https://safe.example.test")
	provider := featureflag.NewStaticProvider()
	provider.Set("internal_mcp_connectors", featureflag.Rule{Default: true})
	router, handler := NewRouterWithOptions(testPool, realtime.NewHub(), events.New(), analytics.NoopClient{}, nil,
		RouterOptions{FeatureFlags: featureflag.NewService(provider)})
	server := httptest.NewServer(router)
	defer server.Close()

	var agentID string
	if err := testPool.QueryRow(t.Context(), `SELECT id::text FROM agent WHERE workspace_id=$1::uuid LIMIT 1`, testWorkspaceID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	endpointID, clientID := uuid.NewString(), uuid.NewString()
	publicAgentID := uuid.NewString()
	if _, err := testPool.Exec(t.Context(), `INSERT INTO agent_a2a_endpoint
		(id,workspace_id,agent_id,public_agent_id,enabled,delegated_by_user_id,card_name)
		VALUES ($1::uuid,$2::uuid,$3::uuid,$4,false,$5::uuid,'Test Agent')`, endpointID, testWorkspaceID, agentID, publicAgentID, testUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_a2a_endpoint WHERE id=$1::uuid`, endpointID)
	})
	if _, err := testPool.Exec(t.Context(), `INSERT INTO a2a_client (id,endpoint_id,name,created_by,updated_by)
		VALUES ($1::uuid,$2::uuid,'Test MCP',$3::uuid,$3::uuid)`, clientID, endpointID, testUserID); err != nil {
		t.Fatal(err)
	}
	capability := "mca2a_" + strings.Repeat("b", 40)
	if _, err := testPool.Exec(t.Context(), `INSERT INTO a2a_client_credential
		(client_id,key_id,token_hash,token_prefix,created_by)
		VALUES ($1::uuid,'test-key',$2,'mca2a_test',$3::uuid)`, clientID, auth.HashToken(capability), testUserID); err != nil {
		t.Fatal(err)
	}
	handler.InternalConnectorClient = &http.Client{Transport: connectorTestTransport{t: t, expectedBearer: capability}}

	input := map[string]any{
		"name": "Hosted Agent", "upstream_url": "https://safe.example.test/api/mcp/connect/" + capability,
		"agent_ids": []string{agentID}, "allowed_tools": []string{}, "auto_discover": true, "enabled": false,
	}
	body, _ := json.Marshal(input)
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/workspaces/"+testWorkspaceID+"/internal-connectors", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var created map[string]any
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil || res.StatusCode != 200 {
		t.Fatalf("capability import failed: status=%d body=%v err=%v", res.StatusCode, created, err)
	}
	id, _ := created["id"].(string)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM internal_connector WHERE id=$1::uuid`, id)
	})
	var storedURL, mode, allowed string
	var ciphertext []byte
	if err := testPool.QueryRow(t.Context(), `SELECT upstream_url,auth_mode,credential_ciphertext,allowed_tools::text
		FROM internal_connector WHERE id=$1::uuid`, id).Scan(&storedURL, &mode, &ciphertext, &allowed); err != nil {
		t.Fatal(err)
	}
	if storedURL != "https://safe.example.test/api/mcp/connect" || mode != "bearer" || len(ciphertext) == 0 ||
		strings.Contains(storedURL, capability) || bytes.Contains(ciphertext, []byte(capability)) || !strings.Contains(allowed, "read") {
		t.Fatalf("capability was not safely normalized: url=%q mode=%q ciphertext_len=%d tools=%s", storedURL, mode, len(ciphertext), allowed)
	}
}

type connectorTestTransport struct {
	t              *testing.T
	empty          bool
	expectedBearer string
}

func (transport connectorTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	expectedBearer := transport.expectedBearer
	if expectedBearer == "" {
		expectedBearer = "workspace-secret"
	}
	if req.URL.Host != "safe.example.test" || req.Header.Get("Authorization") != "Bearer "+expectedBearer || req.Header.Get("X-Task-ID") != "" {
		transport.t.Errorf("connector test escaped fixed target or credential boundary: host=%s", req.URL.Host)
	}
	var rpc struct {
		Method string `json:"method"`
	}
	if err := json.NewDecoder(req.Body).Decode(&rpc); err != nil || rpc.Method != "tools/list" {
		transport.t.Errorf("connectivity test invoked an unexpected method: %q %v", rpc.Method, err)
	}
	response := `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}}]}}`
	if transport.empty {
		response = `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(response))}, nil
}

func TestInternalConnectorCredentialKeySelection(t *testing.T) {
	root := strings.Repeat("j", 48)
	t.Setenv("JWT_SECRET", root)
	t.Setenv("MULTICA_INTERNAL_MCP_SECRET_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("d"), 32)))
	t.Setenv("MULTICA_INTERNAL_MCP_KEY_SOURCE", "jwt-derived")
	first, source, err := internalConnectorCredentialKey()
	if err != nil || source != "jwt-derived" || len(first) != 32 {
		t.Fatalf("derived key unavailable: source=%q length=%d error=%v", source, len(first), err)
	}
	second, _, err := internalConnectorCredentialKey()
	if err != nil || !bytes.Equal(first, second) || bytes.Equal(first, bytes.Repeat([]byte("d"), 32)) {
		t.Fatal("key derivation is unstable or reused the dedicated key")
	}
	t.Setenv("JWT_SECRET", "short")
	if _, _, err := internalConnectorCredentialKey(); err == nil {
		t.Fatal("weak JWT root was accepted")
	}
	t.Setenv("MULTICA_INTERNAL_MCP_KEY_SOURCE", "dedicated")
	key, source, err := internalConnectorCredentialKey()
	if err != nil || source != "dedicated" || !bytes.Equal(key, bytes.Repeat([]byte("d"), 32)) {
		t.Fatalf("dedicated source was not selected: %q %v", source, err)
	}
}

// The desktop external-browser begin link of official app connects is gone:
// it bound whichever browser opened it, so an admin could hand it to someone
// who then authorized their own account into the admin's workspace. The DCR
// callback stays public; the begin path is not routed at all.
func TestConnectorOAuthBeginRouteIsNotRegistered(t *testing.T) {
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	state := "mcpc." + strings.Repeat("A", 43)
	get := func(path string) (int, string) {
		t.Helper()
		resp, err := client.Get(testServer.URL + path + "?state=" + state)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}
	if status, body := get("/api/connector-oauth/begin"); status != http.StatusNotFound {
		t.Fatalf("begin route answered %d: %s", status, body)
	}
	if status, body := get("/api/connector-oauth/callback"); status == http.StatusNotFound || status == http.StatusUnauthorized {
		t.Fatalf("DCR callback route answered %d: %s", status, body)
	}
}
