package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/featureflags"
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
	"github.com/multica-ai/multica/server/internal/service"
)

func TestAgentMCPRejectsMissingCredentialBeforeDatabaseAccess(t *testing.T) {
	h := &Handler{A2AService: &service.A2AService{}}
	withFeatureFlag(t, h, featureflags.AgentA2AInbound, true)

	request := httptest.NewRequest(http.MethodPost, "/api/mcp/agents/public-agent", nil)
	response := httptest.NewRecorder()
	h.HandleAgentMCP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("WWW-Authenticate header is missing")
	}
}

func TestAgentMCPPublishesOnlyDelegationTools(t *testing.T) {
	tools := agentMCPToolDefinitions()
	if len(tools) != 2 {
		t.Fatalf("tool count = %d, want 2", len(tools))
	}
	if tools[0]["name"] != agentMCPDelegateTool || tools[1]["name"] != agentMCPGetTaskTool {
		t.Fatalf("unexpected tools: %#v", tools)
	}
}

func TestAgentMCPDelegateDoesNotDependOnA2APublication(t *testing.T) {
	params, err := json.Marshal(map[string]any{
		"name": agentMCPDelegateTool,
		"arguments": map[string]any{
			"instruction": "Run the delegated task",
		},
	})
	if err != nil {
		t.Fatalf("encode tool call: %v", err)
	}
	h := &Handler{A2AService: &service.A2AService{}}
	request := httptest.NewRequest(http.MethodPost, "/api/mcp/connect/secret", nil)
	response := httptest.NewRecorder()
	h.handleAgentMCPToolsCall(response, request, multicaMCPRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`"call-1"`),
		Method:  "tools/call",
		Params:  params,
	}, a2aintegration.Principal{
		Scopes:                []string{"send"},
		EndpointEnabled:       false,
		AllowDisabledEndpoint: true,
	})

	if strings.Contains(response.Body.String(), "cannot delegate tasks") {
		t.Fatalf("MCP delegate was blocked by A2A publication state: %s", response.Body.String())
	}
}
