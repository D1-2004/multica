package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
