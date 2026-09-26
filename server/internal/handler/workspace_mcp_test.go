package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestWorkspaceMCPListsOnlyScopedToolsAndRejectsOtherWorkspace(t *testing.T) {
	workspaceID := "11111111-1111-4111-8111-111111111111"
	token := db.WorkspaceMCPToken{WorkspaceID: parseUUID(workspaceID), Role: "member", Scopes: []string{"read", "manage"}}
	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("MCP-Protocol-Version", multicaMCPProtocolVersion)
		req.Header.Set("X-Actor-Source", "workspace_mcp_token")
		route := chi.NewRouteContext()
		route.URLParams.Add("workspaceId", path[len("/api/mcp/workspaces/"):])
		req = req.WithContext(middleware.WithWorkspaceMCPPrincipal(
			context.WithValue(req.Context(), chi.RouteCtxKey, route), token))
		rec := httptest.NewRecorder()
		(&Handler{}).WorkspaceMCP(rec, req)
		return rec
	}
	rec := request("/api/mcp/workspaces/" + workspaceID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	foundRuntime := false
	for _, tool := range result.Result.Tools {
		if tool.Name == "runtime_list" {
			foundRuntime = true
		}
		if tool.Name == "agent_create" || tool.Name == "issue_create" {
			t.Fatalf("unexpected tool %s", tool.Name)
		}
	}
	if !foundRuntime {
		t.Fatal("runtime_list not disclosed")
	}
	other := request("/api/mcp/workspaces/22222222-2222-4222-8222-222222222222")
	if other.Code != http.StatusForbidden {
		t.Fatalf("cross-workspace status=%d", other.Code)
	}
}

func TestWorkspaceMCPTokenIssuerRejectsMachineActors(t *testing.T) {
	for _, source := range []string{"workspace_access_token", "workspace_mcp_token", "task_token", "cloud_pat"} {
		req := httptest.NewRequest(http.MethodPost, "/api/workspaces/id/mcp-tokens", nil)
		req.Header.Set("X-Actor-Source", source)
		rec := httptest.NewRecorder()
		RequireWorkspaceMCPHumanIssuer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("machine actor reached token issuance")
		})).ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("source %s status %d", source, rec.Code)
		}
	}
}

func TestWorkspaceMCPRuntimeListDoesNotAdvertiseWorkspaceQuery(t *testing.T) {
	for _, tool := range workspaceMCPTools {
		if tool.name != "runtime_list" {
			continue
		}
		schema := workspaceMCPToolDefinition(tool)["inputSchema"].(map[string]any)
		properties := schema["properties"].(map[string]any)
		if _, hasQuery := properties["query"]; hasQuery {
			t.Fatal("runtime_list must derive workspace from token URL")
		}
		return
	}
	t.Fatal("runtime_list tool missing")
}

func TestWorkspaceMCPRunStatusIncludesTerminalHistory(t *testing.T) {
	for _, tool := range workspaceMCPTools {
		if tool.name == "issue_run_status" {
			if tool.path != "/api/issues/{id}/task-runs" {
				t.Fatalf("run status route = %q", tool.path)
			}
			return
		}
	}
	t.Fatal("issue_run_status tool missing")
}

// workspaceMCPAuditDB accepts the audit INSERTs so tools/call can be driven
// end to end without Postgres.
type workspaceMCPAuditDB struct{}

func (workspaceMCPAuditDB) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (workspaceMCPAuditDB) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return nil, errors.New("unexpected query")
}

func (workspaceMCPAuditDB) QueryRow(context.Context, string, ...interface{}) pgx.Row {
	panic("unexpected query row")
}

type workspaceMCPCallResponse struct {
	Result *struct {
		Content []map[string]json.RawMessage `json:"content"`
		IsError bool                         `json:"isError"`
	} `json:"result"`
	Error *multicaMCPError `json:"error"`
}

func callWorkspaceMCPToolForTest(t *testing.T, dispatcher http.Handler, name string, arguments map[string]any) workspaceMCPCallResponse {
	t.Helper()
	workspaceID := "11111111-1111-4111-8111-111111111111"
	token := db.WorkspaceMCPToken{WorkspaceID: parseUUID(workspaceID), Role: "admin", Scopes: []string{"read", "write", "manage"}}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": arguments}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/mcp/workspaces/"+workspaceID, bytes.NewReader(body))
	req.Header.Set("MCP-Protocol-Version", multicaMCPProtocolVersion)
	req.Header.Set("X-Actor-Source", "workspace_mcp_token")
	route := chi.NewRouteContext()
	route.URLParams.Add("workspaceId", workspaceID)
	req = req.WithContext(middleware.WithWorkspaceMCPPrincipal(
		context.WithValue(req.Context(), chi.RouteCtxKey, route), token))
	rec := httptest.NewRecorder()
	(&Handler{Queries: db.New(workspaceMCPAuditDB{}), WorkspaceMCPDispatcher: dispatcher}).WorkspaceMCP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", name, rec.Code, rec.Body.String())
	}
	var response workspaceMCPCallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("%s: %v body=%s", name, err, rec.Body.String())
	}
	return response
}

func workspaceMCPPathArgs(tool workspaceMCPTool) map[string]any {
	args := map[string]any{}
	for _, segment := range strings.Split(tool.path, "/") {
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") && segment != "{workspaceId}" {
			args[strings.Trim(segment, "{}")] = "33333333-3333-4333-8333-333333333333"
		}
	}
	return args
}

func TestMulticaMCPTextContentAlwaysCarriesText(t *testing.T) {
	encoded, err := json.Marshal([]multicaMCPContent{{Type: "text"}, {Type: "resource", Resource: &agentMCPEmbeddedResource{URI: "multica://x"}}})
	if err != nil {
		t.Fatal(err)
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &blocks); err != nil {
		t.Fatal(err)
	}
	if string(blocks[0]["text"]) != `""` {
		t.Fatalf("empty text content must keep text: %s", encoded)
	}
	if _, hasText := blocks[1]["text"]; hasText {
		t.Fatalf("resource content must not gain text: %s", encoded)
	}
}

func TestWorkspaceMCPEmptyResponsesReturnValidTextContent(t *testing.T) {
	noContent := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	checked := 0
	for _, tool := range workspaceMCPTools {
		if tool.method != http.MethodDelete {
			continue
		}
		args := workspaceMCPPathArgs(tool)
		if tool.body {
			args["payload"] = map[string]any{}
		}
		response := callWorkspaceMCPToolForTest(t, noContent, tool.name, args)
		if response.Error != nil || response.Result == nil {
			t.Fatalf("%s: unexpected error %+v", tool.name, response.Error)
		}
		if response.Result.IsError || len(response.Result.Content) != 1 {
			t.Fatalf("%s: result %+v", tool.name, response.Result)
		}
		var text string
		if err := json.Unmarshal(response.Result.Content[0]["text"], &text); err != nil {
			t.Fatalf("%s: content text must be a string: %v", tool.name, err)
		}
		if text != `{"ok":true,"status":204}` {
			t.Fatalf("%s: text=%q", tool.name, text)
		}
		checked++
	}
	if checked < 5 {
		t.Fatalf("only %d DELETE tools checked", checked)
	}
}

func TestWorkspaceMCPToolsWithoutBodyDoNotRequirePayload(t *testing.T) {
	for _, name := range []string{"issue_cancel_task", "autopilot_trigger"} {
		var tool workspaceMCPTool
		for _, candidate := range workspaceMCPTools {
			if candidate.name == name {
				tool = candidate
			}
		}
		if tool.body {
			t.Fatalf("%s must not declare a body", name)
		}
		var gotMethod, gotPath string
		var gotBody []byte
		dispatcher := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod, gotPath = r.Method, r.URL.Path
			gotBody, _ = io.ReadAll(r.Body)
			writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
		})
		response := callWorkspaceMCPToolForTest(t, dispatcher, name, workspaceMCPPathArgs(tool))
		if response.Error != nil || response.Result == nil || response.Result.IsError {
			t.Fatalf("%s without payload: %+v %+v", name, response.Error, response.Result)
		}
		if gotMethod != http.MethodPost || !strings.HasPrefix(gotPath, "/api/") || len(gotBody) != 0 {
			t.Fatalf("%s dispatched %s %s body=%q", name, gotMethod, gotPath, gotBody)
		}
		args := workspaceMCPPathArgs(tool)
		args["payload"] = map[string]any{}
		response = callWorkspaceMCPToolForTest(t, dispatcher, name, args)
		if response.Error == nil || response.Error.Code != -32602 || response.Error.Message != "unsupported argument: payload" {
			t.Fatalf("%s with payload: %+v", name, response.Error)
		}
	}
}

func TestWorkspaceMCPRerunPayloadIsOptional(t *testing.T) {
	var gotBody []byte
	var gotLength int64
	dispatcher := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLength = r.ContentLength
		gotBody, _ = io.ReadAll(r.Body)
		writeJSON(w, http.StatusOK, map[string]string{"id": "task"})
	})
	issue := "33333333-3333-4333-8333-333333333333"
	response := callWorkspaceMCPToolForTest(t, dispatcher, "issue_rerun", map[string]any{"id": issue})
	if response.Error != nil || response.Result.IsError || gotLength != 0 || len(gotBody) != 0 {
		t.Fatalf("rerun without payload: %+v length=%d body=%q", response.Error, gotLength, gotBody)
	}
	response = callWorkspaceMCPToolForTest(t, dispatcher, "issue_rerun", map[string]any{"id": issue, "payload": map[string]any{"task_id": issue}})
	if response.Error != nil || string(gotBody) != `{"task_id":"`+issue+`"}` {
		t.Fatalf("rerun with task_id: %+v body=%q", response.Error, gotBody)
	}
	response = callWorkspaceMCPToolForTest(t, dispatcher, "issue_rerun", map[string]any{"id": issue, "payload": map[string]any{"other": 1}})
	if response.Error == nil || response.Error.Message != "unsupported payload field: other" {
		t.Fatalf("rerun unknown field: %+v", response.Error)
	}
	response = callWorkspaceMCPToolForTest(t, dispatcher, "issue_create", map[string]any{})
	if response.Error == nil || response.Error.Message != "payload object required" {
		t.Fatalf("issue_create without payload: %+v", response.Error)
	}
}

func TestWorkspaceMCPSchemaRequiresPayloadOnlyForRequiredBodies(t *testing.T) {
	for _, tool := range workspaceMCPTools {
		schema := workspaceMCPToolDefinition(tool)["inputSchema"].(map[string]any)
		_, hasPayload := schema["properties"].(map[string]any)["payload"]
		requiresPayload := false
		for _, key := range schema["required"].([]string) {
			requiresPayload = requiresPayload || key == "payload"
		}
		if hasPayload != tool.body {
			t.Fatalf("%s payload property=%v body=%v", tool.name, hasPayload, tool.body)
		}
		if requiresPayload != (tool.body && !workspaceMCPPayloadOptional(tool.name)) {
			t.Fatalf("%s requires payload=%v", tool.name, requiresPayload)
		}
	}
}

func TestWorkspaceMCPToolCountsByRoleAndScope(t *testing.T) {
	count := func(role string, scopes ...string) int {
		n := 0
		for _, tool := range workspaceMCPTools {
			if workspaceMCPToolAllowed(tool, scopes, role) {
				n++
			}
		}
		return n
	}
	if got := count("admin", "read", "write", "manage"); got != 123 {
		t.Fatalf("admin tools=%d", got)
	}
	if got := count("member", "read", "write", "manage"); got != 79 {
		t.Fatalf("member tools=%d", got)
	}
	if got := count("member", "read"); got != 48 {
		t.Fatalf("read-only tools=%d", got)
	}
}
