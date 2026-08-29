package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/sitehosting"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func testMulticaMCPHandler(t *testing.T) *Handler {
	t.Helper()
	return &Handler{
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

func personalMCPRequest(t *testing.T, method string, id any, params any) *http.Request {
	t.Helper()
	r := mcpRequest(t, method, id, params)
	r.Header.Del("X-Actor-Source")
	r.Header.Del("X-Agent-ID")
	r.Header.Del("X-Task-ID")
	r.Header.Del("X-Workspace-ID")
	r.Header.Set("Authorization", "Bearer mul_test_personal_access_token")
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
	h := testMulticaMCPHandler(t)
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

func TestMulticaMCPToolsListIsAlwaysAvailableAndPublishesAllActions(t *testing.T) {
	h := testMulticaMCPHandler(t)
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
		"search_agents",
		"list_agents",
		"prepare_static_site_deploy",
		"get_static_site_deploy",
	}
	if len(tools) != len(wantNames) {
		t.Fatalf("tools=%#v", tools)
	}
	for index, wantName := range wantNames {
		tool := tools[index].(map[string]any)
		if tool["name"] != wantName {
			t.Fatalf("tool[%d].name=%#v want %q", index, tool["name"], wantName)
		}
		if wantName == multicaMCPPrepareStaticSiteTool || wantName == multicaMCPGetStaticSiteTool {
			outputSchema, ok := tool["outputSchema"].(map[string]any)
			if !ok || outputSchema["type"] != "object" {
				t.Fatalf("%s outputSchema=%#v", wantName, tool["outputSchema"])
			}
		}
		if wantName == multicaMCPChatSendTool || wantName == multicaMCPAgentSearchTool || wantName == multicaMCPAgentListTool {
			continue
		}
		properties := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
		if _, exists := properties["workspace_id"]; exists {
			t.Fatalf("%s exposes caller-controlled workspace_id", wantName)
		}
		if _, exists := properties["task_id"]; exists {
			t.Fatalf("%s exposes caller-controlled task_id", wantName)
		}
		if strings.Contains(wantName, "digital_employee") {
			if _, exists := properties["agent_id"]; !exists {
				t.Fatalf("%s does not expose the PAT Agent selector", wantName)
			}
		} else if _, exists := properties["agent_id"]; exists {
			t.Fatalf("%s exposes caller-controlled agent_id", wantName)
		}
	}
	description, _ := tools[0].(map[string]any)["description"].(string)
	if !strings.Contains(description, "Authorized server-provided Multica action") {
		t.Fatalf("tool description does not identify the managed authorization boundary: %q", description)
	}
}

type fakeSiteHostingService struct {
	prepareInput      sitehosting.PrepareInput
	statusOwnerUserID string
}

func (f *fakeSiteHostingService) Prepare(_ context.Context, input sitehosting.PrepareInput) (sitehosting.PreparedDeploy, error) {
	f.prepareInput = input
	return sitehosting.PreparedDeploy{
		SiteID: "site-id", RevisionID: "revision-id", UploadID: "upload-id",
		UploadPath: "/api/sitehosting/uploads/upload-id", UploadTokenHeader: "X-Multica-Site-Upload-Token",
		UploadURL: "https://api.example.test/api/sitehosting/uploads/upload-id",
		UploadMethod: "PUT", UploadToken: "mhs_secret", ExpiresAt: time.Unix(1_800_000_600, 0).UTC(),
		Archive: "zip", Entrypoint: "index.html", SiteURL: "https://sites.example.test/sites/public-id/",
	}, nil
}

func (f *fakeSiteHostingService) GetStatus(_ context.Context, siteID, ownerUserID string) (sitehosting.SiteStatus, error) {
	f.statusOwnerUserID = ownerUserID
	return sitehosting.SiteStatus{SiteID: siteID, PublicSiteID: "public-id", Status: "active", LatestRevisionID: "revision-id", LatestStatus: "active", SiteURL: "https://sites.example.test/sites/public-id/"}, nil
}

func (f *fakeSiteHostingService) ListSites(context.Context, string) ([]sitehosting.SiteStatus, error) {
	return nil, nil
}

func (f *fakeSiteHostingService) DeleteSite(context.Context, string, string) error {
	return nil
}

func (f *fakeSiteHostingService) HandleUpload(http.ResponseWriter, *http.Request, string) {}
func (f *fakeSiteHostingService) ServePublic(http.ResponseWriter, *http.Request, string, string) {}
func (f *fakeSiteHostingService) ServeFetchProxyRuntime(http.ResponseWriter, *http.Request) {}
func (f *fakeSiteHostingService) HandleFetchProxy(http.ResponseWriter, *http.Request, string) {}

func TestMulticaMCPStaticSiteToolsUseAuthenticatedUserAuthority(t *testing.T) {
	service := &fakeSiteHostingService{}
	h := testMulticaMCPHandler(t)
	h.SiteHosting = service
	response := httptest.NewRecorder()
	h.MulticaMCP(response, mcpRequest(t, "tools/call", "prepare-site", map[string]any{
		"name": "prepare_static_site_deploy",
		"arguments": map[string]any{
			"expected_sha256": strings.Repeat("a", 64),
			"content_length": 1234,
		},
	}))
	if response.Code != http.StatusOK {
		t.Fatalf("prepare status=%d body=%s", response.Code, response.Body.String())
	}
	if service.prepareInput.OwnerUserID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("authority=%#v", service.prepareInput)
	}
	result := decodeMCPResponse(t, response)["result"].(map[string]any)
	structured := result["structuredContent"].(map[string]any)
	if structured["upload_token"] != "mhs_secret" || structured["upload_method"] != "PUT" ||
		structured["upload_path"] != "/api/sitehosting/uploads/upload-id" ||
		structured["upload_token_header"] != "X-Multica-Site-Upload-Token" {
		t.Fatalf("structuredContent=%#v", structured)
	}
	statusResponse := httptest.NewRecorder()
	h.MulticaMCP(statusResponse, mcpRequest(t, "tools/call", "get-site", map[string]any{
		"name": "get_static_site_deploy",
		"arguments": map[string]any{"site_id": "site-id"},
	}))
	statusResult := decodeMCPResponse(t, statusResponse)["result"].(map[string]any)["structuredContent"].(map[string]any)
	if statusResult["site_id"] != "site-id" || statusResult["site_url"] != "https://sites.example.test/sites/public-id/" {
		t.Fatalf("get status=%#v", statusResult)
	}
	if service.statusOwnerUserID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("status owner user=%q", service.statusOwnerUserID)
	}

	personalResponse := httptest.NewRecorder()
	h.MulticaMCP(personalResponse, personalMCPRequest(t, "tools/call", "prepare-site-pat", map[string]any{
		"name": "prepare_static_site_deploy",
		"arguments": map[string]any{"expected_sha256": strings.Repeat("a", 64), "content_length": 1234},
	}))
	if personalResponse.Code != http.StatusOK {
		t.Fatalf("PAT prepare status=%d body=%s", personalResponse.Code, personalResponse.Body.String())
	}
	personalResult := decodeMCPResponse(t, personalResponse)["result"].(map[string]any)
	if personalResult["isError"] == true || service.prepareInput.OwnerUserID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("PAT prepare result=%#v authority=%#v", personalResult, service.prepareInput)
	}
}

type fakeMulticaMCPAgentQueryStore struct {
	workspaces []db.Workspace
	agents     map[string][]db.Agent
	members    map[string]db.Member
	targets    []db.AgentInvocationTarget
}

func (f *fakeMulticaMCPAgentQueryStore) ListWorkspaces(context.Context, pgtype.UUID) ([]db.Workspace, error) {
	return f.workspaces, nil
}

func (f *fakeMulticaMCPAgentQueryStore) GetWorkspace(_ context.Context, id pgtype.UUID) (db.Workspace, error) {
	for _, workspace := range f.workspaces {
		if workspace.ID == id {
			return workspace, nil
		}
	}
	return db.Workspace{}, pgx.ErrNoRows
}

func (f *fakeMulticaMCPAgentQueryStore) ListAgents(_ context.Context, workspaceID pgtype.UUID) ([]db.Agent, error) {
	return f.agents[util.UUIDToString(workspaceID)], nil
}

func (f *fakeMulticaMCPAgentQueryStore) GetMemberByUserAndWorkspace(
	_ context.Context,
	params db.GetMemberByUserAndWorkspaceParams,
) (db.Member, error) {
	member, ok := f.members[util.UUIDToString(params.WorkspaceID)]
	if !ok || member.UserID != params.UserID {
		return db.Member{}, pgx.ErrNoRows
	}
	return member, nil
}

func (f *fakeMulticaMCPAgentQueryStore) ListAgentInvocationTargetsByAgentIDs(
	_ context.Context,
	agentIDs []pgtype.UUID,
) ([]db.AgentInvocationTarget, error) {
	wanted := make(map[pgtype.UUID]struct{}, len(agentIDs))
	for _, agentID := range agentIDs {
		wanted[agentID] = struct{}{}
	}
	var result []db.AgentInvocationTarget
	for _, target := range f.targets {
		if _, ok := wanted[target.AgentID]; ok {
			result = append(result, target)
		}
	}
	return result, nil
}

func TestMulticaMCPAgentQueriesUsePATVisibilityAcrossWorkspaces(t *testing.T) {
	userID := util.MustParseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	workspaceA := util.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	workspaceB := util.MustParseUUID("cccccccc-cccc-cccc-cccc-cccccccccccc")
	ownedAgent := util.MustParseUUID("dddddddd-dddd-dddd-dddd-dddddddddddd")
	privateAgent := util.MustParseUUID("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee")
	sharedAgent := util.MustParseUUID("ffffffff-ffff-ffff-ffff-ffffffffffff")
	adminVisibleAgent := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	otherOwner := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	store := &fakeMulticaMCPAgentQueryStore{
		workspaces: []db.Workspace{
			{ID: workspaceA, Name: "Workspace A", Slug: "workspace-a"},
			{ID: workspaceB, Name: "Workspace B", Slug: "workspace-b"},
		},
		agents: map[string][]db.Agent{
			util.UUIDToString(workspaceA): {
				{ID: ownedAgent, WorkspaceID: workspaceA, OwnerID: userID, Name: "Alpha Probe", Kind: "user", Status: "idle", PermissionMode: "private", Description: "owned"},
				{ID: privateAgent, WorkspaceID: workspaceA, OwnerID: otherOwner, Name: "Hidden Probe", Kind: "user", Status: "idle", PermissionMode: "private"},
				{ID: sharedAgent, WorkspaceID: workspaceA, OwnerID: otherOwner, Name: "Shared PROBE", Kind: "user", Status: "idle", PermissionMode: "public_to"},
			},
			util.UUIDToString(workspaceB): {
				{ID: adminVisibleAgent, WorkspaceID: workspaceB, OwnerID: otherOwner, Name: "Admin Agent", Kind: "user", Status: "idle", PermissionMode: "private"},
			},
		},
		members: map[string]db.Member{
			util.UUIDToString(workspaceA): {UserID: userID, WorkspaceID: workspaceA, Role: "member"},
			util.UUIDToString(workspaceB): {UserID: userID, WorkspaceID: workspaceB, Role: "admin"},
		},
		targets: []db.AgentInvocationTarget{{AgentID: sharedAgent, TargetType: "member", TargetID: userID}},
	}
	h := testMulticaMCPHandler(t)
	h.multicaMCPAgents = store

	listRequest := personalMCPRequest(t, "tools/call", "agent-list", map[string]any{
		"name": multicaMCPAgentListTool, "arguments": map[string]any{},
	})
	listRequest.Header.Set("X-User-ID", util.UUIDToString(userID))
	listResponse := httptest.NewRecorder()
	h.MulticaMCP(listResponse, listRequest)

	result := decodeMCPResponse(t, listResponse)["result"].(map[string]any)
	if isError, _ := result["isError"].(bool); isError {
		t.Fatalf("list_agents returned error: %#v", result)
	}
	agents := result["structuredContent"].(map[string]any)["agents"].([]any)
	if len(agents) != 3 {
		t.Fatalf("agents=%#v", agents)
	}
	encoded, _ := json.Marshal(agents)
	text := string(encoded)
	if strings.Contains(text, util.UUIDToString(privateAgent)) || strings.Contains(text, "mcp_config") || strings.Contains(text, "custom_env") {
		t.Fatalf("list_agents leaked a hidden Agent or secret-bearing fields: %s", text)
	}
	if !strings.Contains(text, `"workspace_name":"Workspace B"`) {
		t.Fatalf("list_agents omitted workspace detail: %s", text)
	}

	searchRequest := personalMCPRequest(t, "tools/call", "agent-search", map[string]any{
		"name": multicaMCPAgentSearchTool, "arguments": map[string]any{"keyword": "probe"},
	})
	searchRequest.Header.Set("X-User-ID", util.UUIDToString(userID))
	searchResponse := httptest.NewRecorder()
	h.MulticaMCP(searchResponse, searchRequest)
	searchResult := decodeMCPResponse(t, searchResponse)["result"].(map[string]any)
	searchAgents := searchResult["structuredContent"].(map[string]any)["agents"].([]any)
	if len(searchAgents) != 2 {
		t.Fatalf("search_agents=%#v", searchAgents)
	}
}

func TestMulticaMCPAgentQueriesKeepTaskTokenInItsWorkspace(t *testing.T) {
	workspaceA := util.MustParseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	workspaceB := util.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	privateAgent := util.MustParseUUID("cccccccc-cccc-cccc-cccc-cccccccccccc")
	otherAgent := util.MustParseUUID("dddddddd-dddd-dddd-dddd-dddddddddddd")
	h := testMulticaMCPHandler(t)
	h.multicaMCPAgents = &fakeMulticaMCPAgentQueryStore{
		workspaces: []db.Workspace{{ID: workspaceA, Name: "Workspace A"}, {ID: workspaceB, Name: "Workspace B"}},
		agents: map[string][]db.Agent{
			util.UUIDToString(workspaceA): {{ID: privateAgent, WorkspaceID: workspaceA, Name: "Private Peer", Kind: "user", PermissionMode: "private"}},
			util.UUIDToString(workspaceB): {{ID: otherAgent, WorkspaceID: workspaceB, Name: "Other Workspace", Kind: "user", PermissionMode: "private"}},
		},
	}
	r := mcpRequest(t, "tools/call", "task-agent-list", map[string]any{
		"name": multicaMCPAgentListTool, "arguments": map[string]any{},
	})
	r.Header.Set("X-Workspace-ID", util.UUIDToString(workspaceA))
	w := httptest.NewRecorder()
	h.MulticaMCP(w, r)

	result := decodeMCPResponse(t, w)["result"].(map[string]any)
	agents := result["structuredContent"].(map[string]any)["agents"].([]any)
	if len(agents) != 1 || agents[0].(map[string]any)["id"] != util.UUIDToString(privateAgent) {
		t.Fatalf("task-token agents=%#v", agents)
	}
}

func TestMulticaMCPSearchAgentsRequiresKeyword(t *testing.T) {
	h := testMulticaMCPHandler(t)
	w := httptest.NewRecorder()
	h.MulticaMCP(w, personalMCPRequest(t, "tools/call", "agent-search-empty", map[string]any{
		"name": multicaMCPAgentSearchTool, "arguments": map[string]any{"keyword": "  "},
	}))

	got := decodeMCPResponse(t, w)
	if got["error"].(map[string]any)["code"] != float64(-32602) {
		t.Fatalf("response=%#v", got)
	}
}

func TestMulticaMCPTransportAndJSONRPCFailures(t *testing.T) {
	h := testMulticaMCPHandler(t)

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

func TestMulticaMCPRequiresSupportedBearer(t *testing.T) {
	t.Run("JWT or cookie-shaped human actor", func(t *testing.T) {
		h := testMulticaMCPHandler(t)
		r := mcpRequest(t, "tools/list", 1, nil)
		r.Header.Del("X-Actor-Source")
		w := httptest.NewRecorder()
		h.MulticaMCP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("personal access token", func(t *testing.T) {
		h := testMulticaMCPHandler(t)
		w := httptest.NewRecorder()
		h.MulticaMCP(w, personalMCPRequest(t, "tools/list", 1, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	})
}

func TestMulticaMCPDigitalEmployeeGetReportsUnconfiguredAsToolError(t *testing.T) {
	h := testMulticaMCPHandler(t)
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
	h := testMulticaMCPHandler(t)
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

func TestMulticaMCPPersonalTokenBindingUsesSelectedAgent(t *testing.T) {
	workspaceID := util.MustParseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	agentID := util.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	userID := util.MustParseUUID("cccccccc-cccc-cccc-cccc-cccccccccccc")
	service := &fakeMulticaMCPBindingService{getResult: agentmessagerouter.DirectDigitalEmployeeBinding{
		WorkspaceID: util.UUIDToString(workspaceID), AgentID: util.UUIDToString(agentID),
		Status: "active", RouterBindingStatus: "valid", RetryStatus: "not_required",
	}}
	h := testMulticaMCPHandler(t)
	h.DigitalEmployeeBindingMCPBindings = service
	h.dingTalkAccountBindingMetadata = &beginBindingMetadataDB{
		agent: db.Agent{
			ID: agentID, WorkspaceID: workspaceID, OwnerID: userID,
			Name: "Agent A", PermissionMode: "private", Kind: "user",
		},
		workspace: db.Workspace{ID: workspaceID, Name: "Workspace A"},
	}
	h.dingTalkAccountBindingPermissions = &fakeDingTalkAccountBindingPermissionStore{
		member: db.Member{UserID: userID, WorkspaceID: workspaceID, Role: "member"},
	}
	r := personalMCPRequest(t, "tools/call", "binding-get-pat", map[string]any{
		"name": multicaMCPBindingGetTool,
		"arguments": map[string]any{
			"agent_id": util.UUIDToString(agentID),
		},
	})
	r.Header.Set("X-User-ID", util.UUIDToString(userID))
	w := httptest.NewRecorder()

	h.MulticaMCP(w, r)

	if w.Code != http.StatusOK || service.getCalls != 1 ||
		service.getWorkspace != workspaceID || service.getAgent != agentID {
		t.Fatalf("status=%d calls=%d workspace=%v agent=%v body=%s", w.Code, service.getCalls, service.getWorkspace, service.getAgent, w.Body.String())
	}
	result := decodeMCPResponse(t, w)["result"].(map[string]any)
	if isError, _ := result["isError"].(bool); isError {
		t.Fatalf("tools/call returned error: %#v", result)
	}
}

func TestMulticaMCPPersonalTokenBindingRequiresAgent(t *testing.T) {
	h := testMulticaMCPHandler(t)
	h.DigitalEmployeeBindingMCPBindings = &fakeMulticaMCPBindingService{}
	w := httptest.NewRecorder()
	h.MulticaMCP(w, personalMCPRequest(t, "tools/call", "binding-get-pat-missing-agent", map[string]any{
		"name":      multicaMCPBindingGetTool,
		"arguments": map[string]any{},
	}))

	result := decodeMCPResponse(t, w)["result"].(map[string]any)
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatalf("expected tool error, got %#v", result)
	}
	content := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(content, "agent_id is required") {
		t.Fatalf("content=%q", content)
	}
}

func TestMulticaMCPWorkspaceMembershipFailsClosed(t *testing.T) {
	userID := util.MustParseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	workspaceID := util.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	allowed := &fakeDingTalkAccountBindingPermissionStore{
		member: db.Member{UserID: userID, WorkspaceID: workspaceID, Role: "member"},
	}
	if err := validateMulticaMCPWorkspaceMember(context.Background(), allowed, userID, workspaceID); err != nil {
		t.Fatalf("valid member rejected: %v", err)
	}
	removed := &fakeDingTalkAccountBindingPermissionStore{memberErr: pgx.ErrNoRows}
	if err := validateMulticaMCPWorkspaceMember(context.Background(), removed, userID, workspaceID); err == nil {
		t.Fatal("removed workspace member was accepted")
	}
}

func TestMulticaMCPTaskTokenBindingRejectsDifferentAgent(t *testing.T) {
	workspaceID := util.MustParseUUID("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	taskAgentID := util.MustParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	requestedAgentID := util.MustParseUUID("cccccccc-cccc-cccc-cccc-cccccccccccc")
	originatorID := util.MustParseUUID("dddddddd-dddd-dddd-dddd-dddddddddddd")
	taskID := util.MustParseUUID("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee")
	service := &fakeMulticaMCPBindingService{}
	h := testMulticaMCPHandler(t)
	h.DigitalEmployeeBindingMCPBindings = service
	h.multicaMCPBindingTasks = &fakeMulticaMCPTaskStore{task: db.AgentTaskQueue{
		ID: taskID, AgentID: taskAgentID, Status: "running", OriginatorUserID: originatorID,
	}}
	h.dingTalkAccountBindingMetadata = &beginBindingMetadataDB{
		agent: db.Agent{
			ID: taskAgentID, WorkspaceID: workspaceID, OwnerID: originatorID,
			Name: "Task Agent", PermissionMode: "private",
		},
		workspace: db.Workspace{ID: workspaceID, Name: "Workspace A"},
	}
	h.dingTalkAccountBindingPermissions = &fakeDingTalkAccountBindingPermissionStore{
		member: db.Member{UserID: originatorID, WorkspaceID: workspaceID, Role: "member"},
	}
	r := mcpRequest(t, "tools/call", "binding-get-task-agent-switch", map[string]any{
		"name": multicaMCPBindingGetTool,
		"arguments": map[string]any{
			"agent_id": util.UUIDToString(requestedAgentID),
		},
	})
	r.Header.Set("X-Workspace-ID", util.UUIDToString(workspaceID))
	r.Header.Set("X-Agent-ID", util.UUIDToString(taskAgentID))
	r.Header.Set("X-Task-ID", util.UUIDToString(taskID))
	w := httptest.NewRecorder()

	h.MulticaMCP(w, r)

	if service.getCalls != 0 {
		t.Fatalf("binding service called %d times", service.getCalls)
	}
	result := decodeMCPResponse(t, w)["result"].(map[string]any)
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatalf("expected tool error, got %#v", result)
	}
	content := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(content, "cannot select a different Agent") {
		t.Fatalf("content=%q", content)
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
	h := testMulticaMCPHandler(t)
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

func TestMulticaMCPPersonalTokenChatSendDoesNotRequireSourceTask(t *testing.T) {
	h := testMulticaMCPHandler(t)
	r := personalMCPRequest(t, "tools/call", "chat-pat-no-source-task", map[string]any{
		"name": multicaMCPChatSendTool,
		"arguments": map[string]any{
			"session_id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
			"content":    "continue the existing Chat",
		},
	})
	r.Header.Set("X-User-ID", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	w := httptest.NewRecorder()

	h.MulticaMCP(w, r)

	result := decodeMCPResponse(t, w)["result"].(map[string]any)
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatalf("expected missing test backend to return a tool error, got %#v", result)
	}
	content := result["content"].([]any)[0].(map[string]any)["text"].(string)
	if strings.Contains(content, "source task") || strings.Contains(content, "workspace") {
		t.Fatalf("personal access token unexpectedly required task or workspace context: %q", content)
	}
}

func TestMulticaMCPPersonalTokenChatSendUsesMemberIdentity(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	targetAgentID := createHandlerTestAgent(t, "MCP PAT target agent", nil)
	targetSessionID := createHandlerTestChatSession(t, targetAgentID)

	r := personalMCPRequest(t, "tools/call", "chat-pat-member", map[string]any{
		"name": multicaMCPChatSendTool,
		"arguments": map[string]any{
			"session_id": targetSessionID,
			"content":    "PAT user continues this Chat",
		},
	})
	r.Header.Set("X-User-ID", testUserID)
	w := httptest.NewRecorder()

	testHandler.MulticaMCP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("tools/call status=%d body=%s", w.Code, w.Body.String())
	}
	result := decodeMCPResponse(t, w)["result"].(map[string]any)
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
		t.Fatalf("load PAT message: %v", err)
	}
	if content != "PAT user continues this Chat" || role != "user" || taskID != structured["task_id"] {
		t.Fatalf("PAT message content=%q role=%q task=%q", content, role, taskID)
	}
	var taskContext []byte
	if err := testPool.QueryRow(ctx, `SELECT context FROM agent_task_queue WHERE id = $1`, structured["task_id"]).Scan(&taskContext); err != nil {
		t.Fatalf("load PAT target task context: %v", err)
	}
	if len(taskContext) > 0 {
		var got map[string]any
		if err := json.Unmarshal(taskContext, &got); err != nil {
			t.Fatalf("decode PAT target task context: %v", err)
		}
		if got[multicaMCPForwardedFromTaskContextKey] != nil ||
			got[multicaMCPForwardedFromSessionContextKey] != nil ||
			got[multicaMCPForwardedFromAgentContextKey] != nil {
			t.Fatalf("PAT task must not carry task-to-task provenance: %#v", got)
		}
	}
}

func TestMulticaMCPChatSendContinuesAnotherOwnedSession(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
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
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "MCP self target agent", nil)
	sessionID := createHandlerTestChatSession(t, agentID)
	taskID := sendDirectChat(t, ctx, agentID, sessionID, "source")
	markTaskRunning(t, ctx, taskID)

	r := mcpRequest(t, "tools/call", 2, map[string]any{
		"name":      multicaMCPChatSendTool,
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
