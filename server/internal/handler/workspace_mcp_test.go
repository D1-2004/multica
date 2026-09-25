package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
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
