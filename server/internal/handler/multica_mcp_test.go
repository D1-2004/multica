package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/util"
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

func TestMulticaMCPToolsListPublishesChatAndSelfDigitalEmployeeActions(t *testing.T) {
	h := testMulticaMCPHandler(t, true)
	w := httptest.NewRecorder()
	h.MulticaMCP(w, mcpRequest(t, "tools/list", "list-1", map[string]any{}))

	if w.Code != http.StatusOK {
		t.Fatalf("tools/list status=%d body=%s", w.Code, w.Body.String())
	}
	got := decodeMCPResponse(t, w)
	tools := got["result"].(map[string]any)["tools"].([]any)
	wantNames := []string{
		multicaMCPChatSendTool,
		"get_digital_employee_binding",
		"bind_digital_employee_to_multica_agent",
		"unbind_digital_employee",
	}
	if len(tools) != len(wantNames) {
		t.Fatalf("tools=%#v", tools)
	}
	for index, wantName := range wantNames {
		tool := tools[index].(map[string]any)
		if tool["name"] != wantName {
			t.Fatalf("tool[%d].name=%#v want %q", index, tool["name"], wantName)
		}
		if wantName == multicaMCPChatSendTool {
			continue
		}
		properties := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
		if _, exists := properties["workspace_id"]; exists {
			t.Fatalf("%s exposes caller-controlled workspace_id", wantName)
		}
		if _, exists := properties["agent_id"]; exists {
			t.Fatalf("%s exposes caller-controlled agent_id", wantName)
		}
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

func TestMulticaMCPDigitalEmployeeGetReportsUnconfiguredAsToolError(t *testing.T) {
	h := testMulticaMCPHandler(t, true)
	w := httptest.NewRecorder()
	h.MulticaMCP(w, mcpRequest(t, "tools/call", "binding-get-1", map[string]any{
		"name":      multicaMCPBindingGetTool,
		"arguments": map[string]any{},
	}))

	if w.Code != http.StatusOK {
		t.Fatalf("tools/call status=%d body=%s", w.Code, w.Body.String())
	}
	got := decodeMCPResponse(t, w)
	if got["error"] != nil {
		t.Fatalf("binding tool failure must use MCP tool result: %#v", got)
	}
	result := got["result"].(map[string]any)
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatalf("expected tool error, got %#v", result)
	}
	content := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(content, "not configured") {
		t.Fatalf("content=%q", content)
	}
}

type fakeMulticaMCPBindingService struct {
	getCalls     int
	getWorkspace pgtype.UUID
	getAgent     pgtype.UUID
	getResult    agentmessagerouter.DirectDigitalEmployeeBinding
	getErr       error
	bindCalls    int
	bindParams   agentmessagerouter.DirectBindingParams
	bindResult   agentmessagerouter.DirectDigitalEmployeeBinding
	bindErr      error
	unbindCalls  int
	unbindParams agentmessagerouter.UnbindParams
	unbindResult agentmessagerouter.PublicDingTalkAccountBinding
	unbindErr    error
}

func (f *fakeMulticaMCPBindingService) GetDigitalEmployeeBinding(
	_ context.Context,
	workspaceID, agentID pgtype.UUID,
) (agentmessagerouter.DirectDigitalEmployeeBinding, error) {
	f.getCalls++
	f.getWorkspace = workspaceID
	f.getAgent = agentID
	return f.getResult, f.getErr
}

func (f *fakeMulticaMCPBindingService) BindDigitalEmployee(
	_ context.Context,
	params agentmessagerouter.DirectBindingParams,
) (agentmessagerouter.DirectDigitalEmployeeBinding, error) {
	f.bindCalls++
	f.bindParams = params
	return f.bindResult, f.bindErr
}

func (f *fakeMulticaMCPBindingService) Unbind(
	_ context.Context,
	params agentmessagerouter.UnbindParams,
) (agentmessagerouter.PublicDingTalkAccountBinding, error) {
	f.unbindCalls++
	f.unbindParams = params
	return f.unbindResult, f.unbindErr
}

type fakeMulticaMCPTaskStore struct {
	task db.AgentTaskQueue
	err  error
}

func (f *fakeMulticaMCPTaskStore) GetAgentTaskInWorkspace(
	context.Context,
	db.GetAgentTaskInWorkspaceParams,
) (db.AgentTaskQueue, error) {
	return f.task, f.err
}

func TestMulticaMCPDigitalEmployeeGetPinsTaskAgentAndUsesHumanOriginator(t *testing.T) {
	workspaceID := util.MustParseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	agentID := util.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	originatorID := util.MustParseUUID("cccccccc-cccc-cccc-cccc-cccccccccccc")
	taskID := util.MustParseUUID("dddddddd-dddd-dddd-dddd-dddddddddddd")
	service := &fakeMulticaMCPBindingService{getResult: agentmessagerouter.DirectDigitalEmployeeBinding{
		WorkspaceID: util.UUIDToString(workspaceID), AgentID: util.UUIDToString(agentID),
		Status: "active", RouterBindingStatus: "valid", RetryStatus: "not_required",
	}}
	h := testMulticaMCPHandler(t, true)
	h.DigitalEmployeeBindingMCPBindings = service
	h.multicaMCPBindingTasks = &fakeMulticaMCPTaskStore{task: db.AgentTaskQueue{
		ID: taskID, AgentID: agentID, Status: "running", OriginatorUserID: originatorID,
	}}
	h.dingTalkAccountBindingMetadata = &beginBindingMetadataDB{
		agent: db.Agent{
			ID: agentID, WorkspaceID: workspaceID, OwnerID: originatorID,
			Name: "Agent A", PermissionMode: "private",
		},
		workspace: db.Workspace{ID: workspaceID, Name: "Workspace A"},
	}
	h.dingTalkAccountBindingPermissions = &fakeDingTalkAccountBindingPermissionStore{
		member: db.Member{UserID: originatorID, WorkspaceID: workspaceID, Role: "member"},
	}
	r := mcpRequest(t, "tools/call", "binding-get-2", map[string]any{
		"name":      multicaMCPBindingGetTool,
		"arguments": map[string]any{},
	})
	r.Header.Set("X-Workspace-ID", util.UUIDToString(workspaceID))
	r.Header.Set("X-Agent-ID", util.UUIDToString(agentID))
	r.Header.Set("X-Task-ID", util.UUIDToString(taskID))
	// The task-token owner is deliberately different. Mutation authority comes
	// from the task's persisted human originator, not this compatibility header.
	r.Header.Set("X-User-ID", "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee")
	w := httptest.NewRecorder()

	h.MulticaMCP(w, r)

	if w.Code != http.StatusOK || service.getCalls != 1 ||
		service.getWorkspace != workspaceID || service.getAgent != agentID {
		t.Fatalf("status=%d get calls=%d workspace=%v agent=%v body=%s", w.Code, service.getCalls, service.getWorkspace, service.getAgent, w.Body.String())
	}
	result := decodeMCPResponse(t, w)["result"].(map[string]any)
	if isError, _ := result["isError"].(bool); isError {
		t.Fatalf("tools/call returned error: %#v", result)
	}
}

func TestMulticaMCPDigitalEmployeeMutationsApplySafeDefaultsAndOriginator(t *testing.T) {
	workspaceID := util.MustParseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	agentID := util.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	originatorID := util.MustParseUUID("cccccccc-cccc-cccc-cccc-cccccccccccc")
	taskID := util.MustParseUUID("dddddddd-dddd-dddd-dddd-dddddddddddd")
	service := &fakeMulticaMCPBindingService{bindResult: agentmessagerouter.DirectDigitalEmployeeBinding{
		WorkspaceID: util.UUIDToString(workspaceID), AgentID: util.UUIDToString(agentID),
		TenantID: "tenant-a", DigitalEmployeeID: "employee-a",
		Status: "active", RouterBindingStatus: "valid", RetryStatus: "not_required",
	}}
	h := testMulticaMCPHandler(t, true)
	h.DigitalEmployeeBindingMCPBindings = service
	h.multicaMCPBindingTasks = &fakeMulticaMCPTaskStore{task: db.AgentTaskQueue{
		ID: taskID, AgentID: agentID, Status: "running", OriginatorUserID: originatorID,
	}}
	h.dingTalkAccountBindingMetadata = &beginBindingMetadataDB{
		agent: db.Agent{
			ID: agentID, WorkspaceID: workspaceID, OwnerID: originatorID,
			Name: "Agent A", PermissionMode: "private",
		},
		workspace: db.Workspace{ID: workspaceID, Name: "Workspace A"},
	}
	h.dingTalkAccountBindingPermissions = &fakeDingTalkAccountBindingPermissionStore{
		member: db.Member{UserID: originatorID, WorkspaceID: workspaceID, Role: "member"},
	}
	r := mcpRequest(t, "tools/call", "binding-bind-1", map[string]any{
		"name": multicaMCPBindingBindTool,
		"arguments": map[string]any{
			"tenant_id": " tenant-a ", "digital_employee_id": " employee-a ",
		},
	})
	r.Header.Set("X-Workspace-ID", util.UUIDToString(workspaceID))
	r.Header.Set("X-Agent-ID", util.UUIDToString(agentID))
	r.Header.Set("X-Task-ID", util.UUIDToString(taskID))
	w := httptest.NewRecorder()

	h.MulticaMCP(w, r)

	if w.Code != http.StatusOK || service.bindCalls != 1 {
		t.Fatalf("status=%d bind calls=%d body=%s", w.Code, service.bindCalls, w.Body.String())
	}
	params := service.bindParams
	if params.Agent.ID != agentID || params.Agent.Workspace.ID != workspaceID || params.InitiatorID != originatorID ||
		params.TenantID != "tenant-a" || params.DigitalEmployeeID != "employee-a" ||
		params.SurfaceType != agentmessagerouter.DingTalkSurfaceAuto ||
		params.MessageScope != agentmessagerouter.DingTalkMessageScopeDirectOnly ||
		len(params.EnabledDomains) != 1 || params.EnabledDomains[0] != "channel" {
		t.Fatalf("bind params=%#v", params)
	}
	secret := "bat_v1.caller-supplied-secret"
	malicious := mcpRequest(t, "tools/call", "binding-bind-2", map[string]any{
		"name": multicaMCPBindingBindTool,
		"arguments": map[string]any{
			"tenant_id": "tenant-a", "digital_employee_id": "employee-a", "bindingToken": secret,
		},
	})
	malicious.Header.Set("X-Workspace-ID", util.UUIDToString(workspaceID))
	malicious.Header.Set("X-Agent-ID", util.UUIDToString(agentID))
	malicious.Header.Set("X-Task-ID", util.UUIDToString(taskID))
	maliciousResponse := httptest.NewRecorder()
	h.MulticaMCP(maliciousResponse, malicious)
	if service.bindCalls != 1 || !strings.Contains(maliciousResponse.Body.String(), `"isError":true`) ||
		strings.Contains(maliciousResponse.Body.String(), secret) {
		t.Fatalf("caller token reached service or response: calls=%d body=%s", service.bindCalls, maliciousResponse.Body.String())
	}
	unbind := mcpRequest(t, "tools/call", "binding-unbind-1", map[string]any{
		"name": multicaMCPBindingUnbindTool, "arguments": map[string]any{},
	})
	unbind.Header.Set("X-Workspace-ID", util.UUIDToString(workspaceID))
	unbind.Header.Set("X-Agent-ID", util.UUIDToString(agentID))
	unbind.Header.Set("X-Task-ID", util.UUIDToString(taskID))
	unbindResponse := httptest.NewRecorder()
	h.MulticaMCP(unbindResponse, unbind)
	if service.unbindCalls != 1 || service.unbindParams.WorkspaceID != workspaceID ||
		service.unbindParams.AgentID != agentID ||
		service.unbindParams.BindingMode != agentmessagerouter.BindingModeMessage {
		t.Fatalf("unbind calls=%d params=%#v body=%s", service.unbindCalls, service.unbindParams, unbindResponse.Body.String())
	}
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
