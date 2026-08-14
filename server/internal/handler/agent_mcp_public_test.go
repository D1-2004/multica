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
	if len(tools) != 7 {
		t.Fatalf("tool count = %d, want 7", len(tools))
	}
	want := []string{
		agentMCPDelegateTool,
		agentMCPGetTaskTool,
		agentMCPGetIssueTool,
		agentMCPContinueIssueTool,
		agentMCPListArtifactsTool,
		agentMCPReadArtifactTool,
		agentMCPDescribeAgentTool,
	}
	for index, name := range want {
		if tools[index]["name"] != name {
			t.Fatalf("tool[%d] = %#v, want %q", index, tools[index], name)
		}
	}
}

func TestAgentMCPDelegateDefaultsToIssueAndMirrorsNativeIssueFields(t *testing.T) {
	args := agentMCPIssueDelegateArguments{Instruction: " Build the report ", Priority: "high"}
	if err := validateAgentMCPIssueArguments(&args); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if args.Mode != "issue" {
		t.Fatalf("mode = %q, want issue", args.Mode)
	}
	if args.Instruction != "Build the report" {
		t.Fatalf("instruction = %q", args.Instruction)
	}
	delegateSchema := agentMCPToolDefinitions()[0]["inputSchema"].(map[string]any)
	properties := delegateSchema["properties"].(map[string]any)
	for _, field := range []string{"title", "priority", "project_id", "parent_issue_id", "stage", "start_date", "due_date", "attachment_ids", "allow_duplicate"} {
		if _, ok := properties[field]; !ok {
			t.Errorf("delegate_task schema missing native Issue field %q", field)
		}
	}
}

func TestAgentMCPIssueTitleFallsBackToFirstInstructionLine(t *testing.T) {
	if got := deriveAgentMCPIssueTitle("", "  First line\nsecond line  "); got != "First line" {
		t.Fatalf("title = %q", got)
	}
}

func TestAgentMCPResourceContentSerializesAsEmbeddedResource(t *testing.T) {
	payload, err := json.Marshal(multicaMCPContent{
		Type: "resource",
		Resource: &agentMCPEmbeddedResource{
			URI: "multica://artifacts/a1", MIMEType: "text/plain", Text: "hello",
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(payload), `"text":""`) || !strings.Contains(string(payload), `"resource"`) {
		t.Fatalf("unexpected resource payload: %s", payload)
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
