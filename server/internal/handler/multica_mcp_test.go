package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/featureflags"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

func testMulticaMCPHandler(t *testing.T, enabled bool) *Handler {
	t.Helper()
	provider := featureflag.NewStaticProvider()
	provider.Set(featureflags.MulticaMCPChatSend, featureflag.Rule{Default: enabled})
	return &Handler{
		FeatureFlags: featureflag.NewService(provider),
		cfg: Config{PublicURL: "https://api.multica.test"},
	}
}

func mcpRequest(t *testing.T, method string, id any, params any) *http.Request {
	t.Helper()
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		body["params"] = params
	}
	r := newRequest(http.MethodPost, "/api/mcp", body)
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", multicaMCPProtocolVersion)
	r.Header.Set("X-Actor-Source", "task_token")
	r.Header.Set("X-User-ID", "00000000-0000-0000-0000-000000000001")
	r.Header.Set("X-Agent-ID", "00000000-0000-0000-0000-000000000002")
	r.Header.Set("X-Task-ID", "00000000-0000-0000-0000-000000000003")
	r.Header.Set("X-Workspace-ID", "00000000-0000-0000-0000-000000000004")
	return r
}

func decodeMCPResponse(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode MCP response: %v; body=%s", err, w.Body.String())
	}
	return got
}

func TestBuildMulticaMCPConfigPreservesExistingServersAndOwnsReservedName(t *testing.T) {
	existing := json.RawMessage(`{"mcpServers":{"fetch":{"command":"uvx"},"multica":{"url":"https://stale.invalid/mcp"}},"experimental":{"keep":true}}`)
	got, injected, err := buildMulticaMCPConfig(existing, "https://api.multica.test/", "mat_secret")
	if err != nil {
		t.Fatal(err)
	}
	if !injected {
		t.Fatal("expected Multica MCP config to be injected")
	}
	var document map[string]any
	if err := json.Unmarshal(got, &document); err != nil {
		t.Fatal(err)
	}
	servers := document["mcpServers"].(map[string]any)
	if servers["fetch"].(map[string]any)["command"] != "uvx" {
		t.Fatalf("existing server was not preserved: %s", got)
	}
	multica := servers["multica"].(map[string]any)
	if multica["type"] != "http" || multica["url"] != "https://api.multica.test/api/mcp" {
		t.Fatalf("unexpected Multica server config: %#v", multica)
	}
	headers := multica["headers"].(map[string]any)
	if headers["Authorization"] != "Bearer mat_secret" {
		t.Fatalf("Authorization header = %#v", headers["Authorization"])
	}
	if document["experimental"].(map[string]any)["keep"] != true {
		t.Fatalf("non-MCP config was not preserved: %s", got)
	}
}

func TestBuildMulticaMCPConfigSkipsWithoutPublicURLOrTaskToken(t *testing.T) {
	existing := json.RawMessage(`{"mcpServers":{"fetch":{"command":"uvx"}}}`)
	for _, tc := range []struct {
		name      string
		publicURL string
		token     string
	}{
		{name: "missing public URL", token: "mat_secret"},
		{name: "missing task token", publicURL: "https://api.multica.test"},
		{name: "wrong token type", publicURL: "https://api.multica.test", token: "mul_member"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, injected, err := buildMulticaMCPConfig(existing, tc.publicURL, tc.token)
			if err != nil {
				t.Fatal(err)
			}
			if injected || string(got) != string(existing) {
				t.Fatalf("got injected=%v config=%s", injected, got)
			}
		})
	}
}

func TestBuildMulticaMCPConfigAcceptsNullServerMap(t *testing.T) {
	got, injected, err := buildMulticaMCPConfig(json.RawMessage(`{"mcpServers":null}`), "https://api.multica.test", "mat_secret")
	if err != nil {
		t.Fatal(err)
	}
	if !injected || !bytes.Contains(got, []byte(`"multica"`)) {
		t.Fatalf("got injected=%v config=%s", injected, got)
	}
}

func TestInjectMulticaMCPIntoClaimHonorsFlagURLAndRuntimeCapability(t *testing.T) {
	request := mcpRequest(t, "tools/list", 1, nil)
	localRuntime := db.AgentRuntime{RuntimeMode: "local", Provider: "codex"}

	t.Run("enabled local runtime", func(t *testing.T) {
		h := testMulticaMCPHandler(t, true)
		resp := AgentTaskResponse{Agent: &TaskAgentData{McpConfig: json.RawMessage(`{"mcpServers":{"fetch":{"command":"uvx"}}}`)}}
		h.injectMulticaMCPIntoClaim(request, &resp, localRuntime, "mat_secret")
		if !bytes.Contains(resp.Agent.McpConfig, []byte(`"multica"`)) || !bytes.Contains(resp.Agent.McpConfig, []byte(`Bearer mat_secret`)) {
			t.Fatalf("Multica MCP was not injected: %s", resp.Agent.McpConfig)
		}
	})

	t.Run("flag disabled", func(t *testing.T) {
		h := testMulticaMCPHandler(t, false)
		resp := AgentTaskResponse{Agent: &TaskAgentData{}}
		h.injectMulticaMCPIntoClaim(request, &resp, localRuntime, "mat_secret")
		if len(resp.Agent.McpConfig) != 0 {
			t.Fatalf("disabled flag injected config: %s", resp.Agent.McpConfig)
		}
	})

	t.Run("public URL missing", func(t *testing.T) {
		h := testMulticaMCPHandler(t, true)
		h.cfg.PublicURL = ""
		resp := AgentTaskResponse{Agent: &TaskAgentData{}}
		h.injectMulticaMCPIntoClaim(request, &resp, localRuntime, "mat_secret")
		if len(resp.Agent.McpConfig) != 0 {
			t.Fatalf("missing public URL injected config: %s", resp.Agent.McpConfig)
		}
	})

	t.Run("cloud runtime without MCP capability", func(t *testing.T) {
		h := testMulticaMCPHandler(t, true)
		runtime := db.AgentRuntime{
			RuntimeMode: "cloud",
			Provider:    "codex",
			Metadata:    []byte(`{"kind":"fc-e2b","provider":"codex","template":"legacy","capabilities":["dws"]}`),
		}
		resp := AgentTaskResponse{Agent: &TaskAgentData{}}
		h.injectMulticaMCPIntoClaim(request, &resp, runtime, "mat_secret")
		if len(resp.Agent.McpConfig) != 0 {
			t.Fatalf("non-MCP cloud runtime received config: %s", resp.Agent.McpConfig)
		}
	})
}

func TestMulticaMCPInitialize(t *testing.T) {
	h := testMulticaMCPHandler(t, true)
	w := httptest.NewRecorder()
	r := mcpRequest(t, "initialize", 1, map[string]any{
		"protocolVersion": multicaMCPProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "1"},
	})

	h.MulticaMCP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("initialize status=%d body=%s", w.Code, w.Body.String())
	}
	got := decodeMCPResponse(t, w)
	result := got["result"].(map[string]any)
	if result["protocolVersion"] != multicaMCPProtocolVersion {
		t.Fatalf("protocolVersion=%#v", result["protocolVersion"])
	}
	capabilities := result["capabilities"].(map[string]any)
	if _, ok := capabilities["tools"]; !ok {
		t.Fatalf("tools capability missing: %#v", capabilities)
	}
}

func TestMulticaMCPToolsListPublishesOnlyChatSend(t *testing.T) {
	h := testMulticaMCPHandler(t, true)
	w := httptest.NewRecorder()
	h.MulticaMCP(w, mcpRequest(t, "tools/list", "list-1", map[string]any{}))

	if w.Code != http.StatusOK {
		t.Fatalf("tools/list status=%d body=%s", w.Code, w.Body.String())
	}
	got := decodeMCPResponse(t, w)
	tools := got["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != multicaMCPChatSendTool {
		t.Fatalf("tools=%#v", tools)
	}
	description, _ := tools[0].(map[string]any)["description"].(string)
	if !strings.Contains(description, "Authorized server-provided Multica action") {
		t.Fatalf("tool description does not identify the managed authorization boundary: %q", description)
	}
}

func TestMulticaMCPTransportAndJSONRPCFailures(t *testing.T) {
	h := testMulticaMCPHandler(t, true)

	t.Run("GET returns 405", func(t *testing.T) {
		r := mcpRequest(t, "tools/list", 1, nil)
		r.Method = http.MethodGet
		w := httptest.NewRecorder()
		h.MulticaMCP(w, r)
		if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != http.MethodPost {
			t.Fatalf("status=%d allow=%q body=%s", w.Code, w.Header().Get("Allow"), w.Body.String())
		}
	})

	t.Run("unknown method returns JSON-RPC error", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.MulticaMCP(w, mcpRequest(t, "resources/list", "unknown-1", nil))
		got := decodeMCPResponse(t, w)
		if got["id"] != "unknown-1" || got["error"].(map[string]any)["code"] != float64(-32601) {
			t.Fatalf("response=%#v", got)
		}
	})

	t.Run("notification has no response", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.MulticaMCP(w, mcpRequest(t, "notifications/initialized", nil, nil))
		if w.Code != http.StatusAccepted || w.Body.Len() != 0 {
			t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
		}
	})

	t.Run("untrusted Origin is rejected", func(t *testing.T) {
		r := mcpRequest(t, "tools/list", 1, nil)
		r.Header.Set("Origin", "https://attacker.invalid")
		w := httptest.NewRecorder()
		h.MulticaMCP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	})
}

func TestMulticaMCPRequiresTaskTokenActorAndReleaseFlag(t *testing.T) {
	t.Run("human actor", func(t *testing.T) {
		h := testMulticaMCPHandler(t, true)
		r := mcpRequest(t, "tools/list", 1, nil)
		r.Header.Del("X-Actor-Source")
		w := httptest.NewRecorder()
		h.MulticaMCP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("flag disabled", func(t *testing.T) {
		h := testMulticaMCPHandler(t, false)
		w := httptest.NewRecorder()
		h.MulticaMCP(w, mcpRequest(t, "tools/list", 1, nil))
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	})
}

func TestMulticaMCPChatSendContinuesAnotherOwnedSession(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.MulticaMCPChatSend, true)
	ctx := context.Background()
	sourceAgentID := createHandlerTestAgent(t, "MCP source agent", nil)
	sourceSessionID := createHandlerTestChatSession(t, sourceAgentID)
	sourceTaskID := sendDirectChat(t, ctx, sourceAgentID, sourceSessionID, "find the answer")
	markTaskRunning(t, ctx, sourceTaskID)
	targetAgentID := createHandlerTestAgent(t, "MCP target agent", nil)
	targetSessionID := createHandlerTestChatSession(t, targetAgentID)

	r := mcpRequest(t, "tools/call", "call-1", map[string]any{
		"name": multicaMCPChatSendTool,
		"arguments": map[string]any{
			"session_id": targetSessionID,
			"content":    "  这是 B 对话所问问题的答案  ",
		},
	})
	r.Header.Set("X-User-ID", testUserID)
	r.Header.Set("X-Agent-ID", sourceAgentID)
	r.Header.Set("X-Task-ID", sourceTaskID)
	r.Header.Set("X-Workspace-ID", testWorkspaceID)
	r = withChatTestWorkspaceCtx(t, r)
	w := httptest.NewRecorder()

	testHandler.MulticaMCP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("tools/call status=%d body=%s", w.Code, w.Body.String())
	}
	got := decodeMCPResponse(t, w)
	result := got["result"].(map[string]any)
	if isError, _ := result["isError"].(bool); isError {
		t.Fatalf("tools/call returned tool error: %#v", result)
	}
	structured := result["structuredContent"].(map[string]any)
	if structured["session_id"] != targetSessionID || structured["task_id"] == "" || structured["message_id"] == "" {
		t.Fatalf("structuredContent=%#v", structured)
	}

	var content, role, taskID string
	if err := testPool.QueryRow(ctx, `
		SELECT content, role, task_id::text
		FROM chat_message
		WHERE id = $1 AND chat_session_id = $2
	`, structured["message_id"], targetSessionID).Scan(&content, &role, &taskID); err != nil {
		t.Fatalf("load forwarded message: %v", err)
	}
	if content != "  这是 B 对话所问问题的答案  " || role != "user" || taskID != structured["task_id"] {
		t.Fatalf("forwarded message content=%q role=%q task=%q", content, role, taskID)
	}
	var taskContext []byte
	if err := testPool.QueryRow(ctx, `SELECT context FROM agent_task_queue WHERE id = $1`, structured["task_id"]).Scan(&taskContext); err != nil {
		t.Fatalf("load target task context: %v", err)
	}
	var provenance map[string]any
	if err := json.Unmarshal(taskContext, &provenance); err != nil {
		t.Fatalf("decode target task context: %v", err)
	}
	if provenance[multicaMCPForwardedFromTaskContextKey] != sourceTaskID ||
		provenance[multicaMCPForwardedFromSessionContextKey] != sourceSessionID ||
		provenance[multicaMCPForwardedFromAgentContextKey] != sourceAgentID {
		t.Fatalf("forward provenance=%#v", provenance)
	}
}

func TestMulticaMCPChatSendRejectsSourceSession(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	withFeatureFlag(t, testHandler, featureflags.MulticaMCPChatSend, true)
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "MCP self target agent", nil)
	sessionID := createHandlerTestChatSession(t, agentID)
	taskID := sendDirectChat(t, ctx, agentID, sessionID, "source")
	markTaskRunning(t, ctx, taskID)

	r := mcpRequest(t, "tools/call", 2, map[string]any{
		"name": multicaMCPChatSendTool,
		"arguments": map[string]any{"session_id": sessionID, "content": "loop"},
	})
	r.Header.Set("X-User-ID", testUserID)
	r.Header.Set("X-Agent-ID", agentID)
	r.Header.Set("X-Task-ID", taskID)
	r.Header.Set("X-Workspace-ID", testWorkspaceID)
	r = withChatTestWorkspaceCtx(t, r)
	w := httptest.NewRecorder()
	testHandler.MulticaMCP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("tools/call status=%d body=%s", w.Code, w.Body.String())
	}
	result := decodeMCPResponse(t, w)["result"].(map[string]any)
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatalf("expected tool error, got %#v", result)
	}
}
