package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/featureflags"
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
