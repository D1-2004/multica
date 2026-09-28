package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/util/secretbox"
)

func TestInternalConnectorHostAndCredentialIsolation(t *testing.T) {
	t.Setenv("MULTICA_INTERNAL_MCP_ALLOWED_HOST_SUFFIXES", "pre-wiki.dingtalk.alibaba-inc.com")
	good := connectorInput{Name: "Knowledge", UpstreamURL: "https://pre-wiki.dingtalk.alibaba-inc.com/mcp", AllowedTools: []string{"query"}}
	if err := validateConnectorInput(good); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{"http://pre-wiki.dingtalk.alibaba-inc.com/mcp", "https://pre-wiki.dingtalk.alibaba-inc.com.evil.test/mcp", "https://name@pre-wiki.dingtalk.alibaba-inc.com/mcp", "https://pre-wiki.dingtalk.alibaba-inc.com:8443/mcp"} {
		bad := good
		bad.UpstreamURL = url
		if validateConnectorInput(bad) == nil {
			t.Fatalf("accepted upstream %s", url)
		}
	}
	a := "11111111-1111-4111-8111-111111111111"
	b := "22222222-2222-4222-8222-222222222222"
	t.Setenv(connectorCredentialRef(a), "token-a")
	if !connectorCredentialReady(internalConnector{ID: a, CredentialRef: connectorCredentialRef(a)}) {
		t.Fatal("approved credential not found")
	}
	if connectorCredentialReady(internalConnector{ID: b, CredentialRef: connectorCredentialRef(b)}) || connectorCredentialReady(internalConnector{ID: b, CredentialRef: connectorCredentialRef(a)}) {
		t.Fatal("credential crossed connector boundary")
	}
}

func TestInternalConnectorStoredCredentialIsBoundToWorkspaceAndConnector(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	workspace := "33333333-3333-4333-8333-333333333333"
	box, err := secretbox.New(bytes.Repeat([]byte("k"), secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal([]byte(`{"workspace_id":"` + workspace + `","connector_id":"` + id + `","bearer":"stored-token"}`))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(connectorCredentialRef(id), "environment-token")
	h := &Handler{InternalConnectorSecretBox: box}
	c := internalConnector{ID: id, WorkspaceID: workspace, CredentialRef: connectorCredentialRef(id), CredentialCiphertext: sealed}
	if token, err := h.connectorBearer(c); err != nil || token != "stored-token" {
		t.Fatalf("stored credential not selected: %q %v", token, err)
	}
	if _, err := (&Handler{}).connectorBearer(c); err == nil {
		t.Fatal("missing decryption key fell back to environment credential")
	}
	c.WorkspaceID = "44444444-4444-4444-8444-444444444444"
	if _, err := h.connectorBearer(c); err == nil {
		t.Fatal("credential crossed workspace boundary or fell back to environment")
	}
	c.WorkspaceID = workspace
	c.ID = "22222222-2222-4222-8222-222222222222"
	c.CredentialRef = connectorCredentialRef(c.ID)
	if _, err := h.connectorBearer(c); err == nil {
		t.Fatal("credential crossed connector boundary")
	}
}

func TestInternalConnectorRelayFiltersToolsAndHeaders(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	t.Setenv(connectorCredentialRef(id), "upstream-only")
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream-only" || r.Header.Get("X-Task-ID") != "" {
			t.Error("credential boundary violated")
		}
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid outbound JSON")
			return
		}
		if req.Method == "tools/list" {
			if req.Params["cursor"] != "page2" {
				t.Error("list cursor dropped")
			}
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read_knowledge","inputSchema":{"type":"object"}},{"name":"delete_knowledge"}],"nextCursor":"page3"}}`)
		} else {
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"real result"}]}}`)
		}
	}))
	defer upstream.Close()
	h := &Handler{InternalConnectorClient: upstream.Client()}
	c := internalConnector{ID: id, CredentialRef: connectorCredentialRef(id), UpstreamURL: upstream.URL, AllowedTools: []string{"read_knowledge"}}
	result, err := h.callInternalConnectorUpstream(context.Background(), c, "tools/list", connectorRPCParams{Cursor: "page2"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "delete_knowledge") || !strings.Contains(string(encoded), "page3") {
		t.Fatalf("list boundary broken: %s", encoded)
	}
	result, err = h.callInternalConnectorUpstream(context.Background(), c, "tools/call", connectorRPCParams{Name: "read_knowledge", Arguments: json.RawMessage(`{}`)})
	toolResult, ok := result.(multicaMCPToolResult)
	if err != nil || !ok || len(toolResult.Content) != 1 || toolResult.Content[0].Text != "real result" {
		t.Fatalf("call failed: %#v %v", result, err)
	}
}

func TestInternalConnectorRateKeysAreIsolated(t *testing.T) {
	t.Setenv("AONE_ENV_TYPE", "pre")
	rates := &semanticaTestRates{n: 1}
	h := &Handler{InternalConnectorRedis: rates}
	if err := h.connectorLimit(context.Background(), "11111111-1111-4111-8111-111111111111", "task", "agent", "workspace"); err != nil {
		t.Fatal(err)
	}
	if len(rates.keys) != 3 {
		t.Fatalf("wrong scopes: %v", rates.keys)
	}
	for _, key := range rates.keys {
		if !strings.HasPrefix(key, "mcpconn:pre:connector:11111111-1111-4111-8111-111111111111:rate:") {
			t.Fatal(key)
		}
	}
}

func TestInternalConnectorUpstreamToolErrorRetainsBoundedText(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	t.Setenv(connectorCredentialRef(id), "upstream-only")
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"invalid cypher syntax"}]}}`)
	}))
	defer upstream.Close()
	h := &Handler{InternalConnectorClient: upstream.Client()}
	c := internalConnector{ID: id, CredentialRef: connectorCredentialRef(id), UpstreamURL: upstream.URL}
	result, err := h.callInternalConnectorUpstream(context.Background(), c, "tools/call", connectorRPCParams{Name: "read_knowledge", Arguments: json.RawMessage(`{}`)})
	toolResult, ok := result.(multicaMCPToolResult)
	if err != nil || !ok || !toolResult.IsError || len(toolResult.Content) != 1 || toolResult.Content[0].Text != "invalid cypher syntax" {
		t.Fatalf("tool error text lost: %#v %v", result, err)
	}
}

func TestConnectorTestFailureMessageIsActionableWithoutUpstreamBody(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{connectorUpstreamStatusError{Code: 401}, "upstream rejected credential (HTTP 401)"},
		{connectorUpstreamStatusError{Code: 502}, "upstream returned HTTP 502"},
		{connectorUpstreamProtocolError{}, "upstream returned an invalid MCP response"},
		{context.DeadlineExceeded, "upstream request timed out"},
		{errors.New("secret body from remote"), "upstream network connection failed"},
	} {
		if got := connectorTestFailureMessage(tc.err); got != tc.want || strings.Contains(got, "secret") {
			t.Fatalf("failure category mismatch or leaked upstream detail: got %q want %q", got, tc.want)
		}
	}
}

func TestInternalConnectorToolNamesStayWithinPiLimit(t *testing.T) {
	t.Setenv("MULTICA_INTERNAL_MCP_ALLOWED_HOST_SUFFIXES", "pre-wiki.dingtalk.alibaba-inc.com")
	id := "a3fc1b87-7e59-452f-951d-7a317e110709"
	server := connectorServerName(id)
	if server != "ca3fc1b877e59452f" {
		t.Fatalf("unstable connector server name: %q", server)
	}
	for _, original := range []string{"get_knowledge_graph_schema", "get_knowledge_node_schema", "query_knowledge_cypher", "search_knowledge", strings.Repeat("long_tool_", 12)} {
		presented := connectorPresentedToolName(original)
		if n := len("mcp__" + server + "__" + presented); n > 62 {
			t.Fatalf("Pi MCP name remains too long: %d %q", n, presented)
		}
		if resolved, ok := connectorOriginalAllowedTool([]string{original}, presented); !ok || resolved != original {
			t.Fatalf("alias did not resolve back to the allowed upstream tool: %q", presented)
		}
	}
	longName := strings.Repeat("long_tool_", 12)
	if err := validateConnectorInput(connectorInput{Name: "Knowledge", UpstreamURL: "https://pre-wiki.dingtalk.alibaba-inc.com/mcp", AllowedTools: []string{longName, connectorPresentedToolName(longName)}}); err == nil {
		t.Fatal("alias collision was accepted")
	}
}

func TestInternalConnectorLongToolListUsesResolvableAlias(t *testing.T) {
	id := "a3fc1b87-7e59-452f-951d-7a317e110709"
	longName := strings.Repeat("read_knowledge_", 6)
	t.Setenv(connectorCredentialRef(id), "upstream-only")
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Method != "tools/list" {
			t.Errorf("unexpected upstream request: %q %v", request.Method, err)
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":%q,"description":"Read knowledge","inputSchema":{"type":"object"}}]}}`, longName)
	}))
	defer upstream.Close()
	h := &Handler{InternalConnectorClient: upstream.Client()}
	c := internalConnector{ID: id, CredentialRef: connectorCredentialRef(id), UpstreamURL: upstream.URL, AllowedTools: []string{longName}}
	result, err := h.callInternalConnectorUpstream(context.Background(), c, "tools/list", connectorRPCParams{})
	if err != nil {
		t.Fatal(err)
	}
	list := result.(map[string]any)
	tools := list["tools"].([]map[string]any)
	if len(tools) != 1 {
		t.Fatalf("wrong filtered tool list: %#v", tools)
	}
	alias, _ := tools[0]["name"].(string)
	if alias == longName || alias != connectorPresentedToolName(longName) || !strings.Contains(tools[0]["description"].(string), longName) {
		t.Fatalf("long tool was not presented under a documented alias: %#v", tools[0])
	}
	if original, ok := connectorOriginalAllowedTool(c.AllowedTools, alias); !ok || original != longName {
		t.Fatal("presented alias did not resolve to the authorized upstream tool")
	}
}
