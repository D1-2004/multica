package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

func TestInternalConnectorRelayPreservesToolsAndIsolatesHeaders(t *testing.T) {
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
	if !strings.Contains(string(encoded), "delete_knowledge") || !strings.Contains(string(encoded), "page3") {
		t.Fatalf("list boundary broken: %s", encoded)
	}
	result, err = h.callInternalConnectorUpstream(context.Background(), c, "tools/call", connectorRPCParams{Name: "read_knowledge", Arguments: json.RawMessage(`{}`)})
	toolResult, ok := result.(multicaMCPToolResult)
	if err != nil || !ok || len(toolResult.Content) != 1 || toolResult.Content[0].Text != "real result" {
		t.Fatalf("call failed: %#v %v", result, err)
	}
}

func TestInternalConnectorNoAuthDiscoveryIncludesAllAnnotations(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	ws := "33333333-3333-4333-8333-333333333333"
	t.Setenv(connectorCredentialRef(id), "must-not-leak")
	t.Setenv("MULTICA_INTERNAL_MCP_ALLOWED_HOST_SUFFIXES", "pre-wiki.dingtalk.alibaba-inc.com")
	h := &Handler{InternalConnectorClient: &http.Client{Transport: connectorTestRoundTrip(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("no-auth upstream received Authorization: %q", got)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"delegate_task","annotations":{"readOnlyHint":false}},{"name":"describe_agent","annotations":{"readOnlyHint":true}}]}}`))}, nil
	})}}
	in := connectorInput{Name: "Agent", UpstreamURL: "https://pre-wiki.dingtalk.alibaba-inc.com/mcp", AuthMode: "none", AutoDiscover: true}
	sealed, err := h.prepareInternalConnectorCreate(context.Background(), &in, id, ws)
	if err != nil || len(sealed) != 0 || in.AuthMode != "none" || len(in.AllowedTools) != 2 || in.AllowedTools[0] != "delegate_task" {
		t.Fatalf("no-auth setup failed: tools=%v mode=%q ciphertext=%d err=%v", in.AllowedTools, in.AuthMode, len(sealed), err)
	}
	if err := validateConnectorInput(in); err != nil {
		t.Fatal(err)
	}
	if !h.connectorCredentialReady(internalConnector{ID: id, WorkspaceID: ws, CredentialRef: connectorCredentialRef(id), AuthMode: "none"}) {
		t.Fatal("no-auth connector was treated as missing a credential")
	}
}

func TestInternalConnectorAdminToolListRetriesOnlyTimeouts(t *testing.T) {
	c := internalConnector{ID: "11111111-1111-4111-8111-111111111111", AuthMode: "none", UpstreamURL: "https://safe.example.test/mcp", AllowedTools: []string{"read"}}
	calls := 0
	h := &Handler{InternalConnectorClient: &http.Client{Transport: connectorTestRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, context.DeadlineExceeded
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read"}]}}`))}, nil
	})}}
	result, attempts, err := h.connectorToolListWithRetry(context.Background(), c, "")
	if err != nil || attempts != 2 || calls != 2 || len(result.(map[string]any)["tools"].([]map[string]any)) != 1 {
		t.Fatalf("timed-out tools/list did not recover once: attempts=%d calls=%d result=%v err=%v", attempts, calls, result, err)
	}

	calls = 0
	h.InternalConnectorClient = &http.Client{Transport: connectorTestRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("rejected"))}, nil
	})}
	_, attempts, err = h.connectorToolListWithRetry(context.Background(), c, "")
	if attempts != 1 || calls != 1 || err == nil {
		t.Fatalf("credential rejection was retried: attempts=%d calls=%d err=%v", attempts, calls, err)
	}
}

func TestInternalConnectorCapabilityLinkAcceptsAnyApprovedDeployment(t *testing.T) {
	t.Setenv("MULTICA_INTERNAL_MCP_ALLOWED_HOST_SUFFIXES", "dingtalk.com,alibaba-inc.com")
	token := "mca2a_" + strings.Repeat("a", 40)
	for _, base := range []string{"https://pre-fde-workbench.dingtalk.com", "https://fde-workbench.dingtalk.com/base"} {
		canonical, got, err := parseInternalConnectorCapabilityLink(base + "/api/mcp/connect/" + token)
		if err != nil || got != token || canonical != base+"/api/mcp/connect" {
			t.Fatalf("approved capability rejected or not redacted: err=%v", err)
		}
	}
	for _, raw := range []string{"https://evil.test/api/mcp/connect/" + token, "https://fde-workbench.dingtalk.com/api/mcp/connect/" + token + "/extra"} {
		if _, _, err := parseInternalConnectorCapabilityLink(raw); err == nil || strings.Contains(err.Error(), token) {
			t.Fatal("invalid capability accepted or leaked")
		}
	}
}

type connectorTestRoundTrip func(*http.Request) (*http.Response, error)

func (f connectorTestRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

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
		if resolved, ok := connectorOriginalTool([]string{original}, presented); !ok || resolved != original {
			t.Fatalf("alias did not resolve back to the allowed upstream tool: %q", presented)
		}
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
	if original, ok := connectorOriginalTool(c.AllowedTools, alias); !ok || original != longName {
		t.Fatal("presented alias did not resolve to the authorized upstream tool")
	}
}

func TestInternalConnectorCapabilityDiscoveryUsesRemoteCredentialAndAllTools(t *testing.T) {
	const id = "11111111-1111-4111-8111-111111111111"
	const ws = "33333333-3333-4333-8333-333333333333"
	t.Setenv("MULTICA_INTERNAL_MCP_ALLOWED_HOST_SUFFIXES", "dingtalk.com,alibaba-inc.com")
	box, err := secretbox.New(bytes.Repeat([]byte("k"), secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{0, 70} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			token := "remote-capability-secret"
			h := &Handler{InternalConnectorSecretBox: box, InternalConnectorClient: &http.Client{Transport: connectorTestRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "pre-fde-workbench.dingtalk.com" || r.URL.Path != "/api/mcp/connect/"+token || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("MCP-Protocol-Version") == "" {
					t.Fatal("capability request lost its target, credential or protocol header")
				}
				tools := make([]map[string]any, count)
				for i := range tools {
					tools[i] = map[string]any{"name": fmt.Sprintf("tool_%d", i), "annotations": map[string]any{"readOnlyHint": false}}
				}
				raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"tools": tools}})
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw))}, nil
			})}}
			in := connectorInput{Name: "Remote", UpstreamURL: "https://pre-fde-workbench.dingtalk.com/api/mcp/connect/" + token, AutoDiscover: true}
			sealed, err := h.prepareInternalConnectorCreate(t.Context(), &in, id, ws)
			if err != nil || len(in.AllowedTools) != count || strings.Contains(in.UpstreamURL, token) || bytes.Contains(sealed, []byte(token)) {
				t.Fatalf("remote discovery failed or leaked credential: count=%d err=%v", len(in.AllowedTools), err)
			}
			if err := validateConnectorInput(in); err != nil {
				t.Fatal(err)
			}
		})
	}
}
