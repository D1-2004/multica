package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/handler"
)

func TestPrepareStaticSiteToolDescribesPageConfiguredFetchProxy(t *testing.T) {
	h := &handler.Handler{}
	body := []byte(`{"jsonrpc":"2.0","id":"tools","method":"tools/list","params":{}}`)
	request := httptest.NewRequest(http.MethodPost, "/api/mcp", bytes.NewReader(body))
	request.Header.Set("X-Actor-Source", "task_token")
	request.Header.Set("MCP-Protocol-Version", "2025-06-18")
	response := httptest.NewRecorder()

	h.MulticaMCP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				InputSchema struct {
					Properties map[string]any `json:"properties"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, tool := range payload.Result.Tools {
		if tool.Name != "prepare_static_site_deploy" {
			continue
		}
		for _, required := range []string{
			"window.__MULTICA_FETCH_PROXY_ALLOWLIST__",
			"exact HTTPS URLs",
			"ordinary window.fetch",
			"same-origin proxy",
			"unmatched requests continue to use native fetch",
			"allowed target origins and SSRF protections",
		} {
			if !strings.Contains(tool.Description, required) {
				t.Fatalf("description is missing %q: %q", required, tool.Description)
			}
		}
		if len(tool.InputSchema.Properties) != 5 {
			t.Fatalf("fetch proxy documentation must not add schema arguments: %#v", tool.InputSchema.Properties)
		}
		return
	}
	t.Fatal("prepare_static_site_deploy not found")
}
