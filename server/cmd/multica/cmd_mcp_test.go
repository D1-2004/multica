package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPCommandExposesOnlyGenericDiscoveryAndCallSurface(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"mcp", "tools"})
	if err != nil || cmd == nil || cmd.CommandPath() != "multica mcp tools" {
		t.Fatalf("multica mcp tools is not registered: cmd=%v err=%v", cmd, err)
	}

	cmd, _, err = rootCmd.Find([]string{"mcp", "call"})
	if err != nil || cmd == nil || cmd.CommandPath() != "multica mcp call" {
		t.Fatalf("multica mcp call is not registered: cmd=%v err=%v", cmd, err)
	}
	if cmd.Flags().Lookup("method") == nil {
		t.Fatal("multica mcp call must accept a generic --method flag")
	}

	if concrete, _, findErr := rootCmd.Find([]string{"mcp", "search_agents"}); findErr == nil && concrete != nil && concrete.CommandPath() == "multica mcp search_agents" {
		t.Fatal("MCP methods must not be compiled into CLI subcommands")
	}
}

func TestMCPToolsDiscoversMethodsFromServerAtRuntime(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "mat_current_task")
	t.Setenv("MULTICA_TASK_ID", "task-1")
	t.Setenv("MULTICA_AGENT_ID", "agent-1")
	t.Setenv("MULTICA_WORKSPACE_ID", "workspace-must-not-be-required")

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if got := r.Header.Get("Authorization"); got != "Bearer mat_current_task" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Task-ID"); got != "task-1" {
			t.Errorf("X-Task-ID = %q", got)
		}
		if got := r.Header.Get("X-Workspace-ID"); got != "" {
			t.Errorf("X-Workspace-ID must not be required by MCP, got %q", got)
		}

		var request struct {
			JSONRPC string         `json:"jsonrpc"`
			ID      int            `json:"id"`
			Method  string         `json:"method"`
			Params  map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if request.Method != "tools/list" {
			t.Errorf("unexpected MCP request method %q", request.Method)
			return
		}
		if got := r.Header.Get("MCP-Protocol-Version"); got != mcpCLIProtocolVersion {
			t.Errorf("MCP-Protocol-Version = %q", got)
		}
		if requestCount == 1 {
			if _, exists := request.Params["cursor"]; exists {
				t.Errorf("first tools/list must not send a cursor: %#v", request.Params)
			}
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"method_added_after_image_build","description":"Discovered from the server","inputSchema":{"type":"object"}}],"nextCursor":"page-2"}}`, request.ID)
			return
		}
		if request.Params["cursor"] != "page-2" {
			t.Errorf("second tools/list cursor = %#v", request.Params["cursor"])
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"second_dynamic_method","description":"Second page","inputSchema":{"type":"object"}}]}}`, request.ID)
	}))
	defer server.Close()
	t.Setenv("MULTICA_SERVER_URL", server.URL)

	cmd := newMCPToolsCommand()
	out, err := captureStdout(t, func() error { return cmd.RunE(cmd, nil) })
	if err != nil {
		t.Fatalf("mcp tools: %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("request count = %d, want two paginated tools/list requests and no initialize", requestCount)
	}
	if !strings.Contains(out, `"method_added_after_image_build"`) || !strings.Contains(out, `"second_dynamic_method"`) || !strings.Contains(out, `"inputSchema"`) {
		t.Fatalf("mcp tools output did not preserve the server definition: %s", out)
	}
}

func TestMCPCallForwardsArbitraryMethodNameAndArguments(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_SERVER_URL", "")
	t.Setenv("MULTICA_TOKEN", "mat_current_task")
	t.Setenv("MULTICA_TASK_ID", "task-1")

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if request.Method != "tools/call" {
			t.Errorf("request method = %q, want tools/call", request.Method)
		}
		if got := r.Header.Get("MCP-Protocol-Version"); got != mcpCLIProtocolVersion {
			t.Errorf("MCP-Protocol-Version = %q", got)
		}
		if request.Params.Name != "method_added_after_image_build" {
			t.Errorf("tool name = %q", request.Params.Name)
		}
		if request.Params.Arguments["keyword"] != "dynamic" {
			t.Errorf("arguments = %#v", request.Params.Arguments)
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"ok"}],"structuredContent":{"source":"server"}}}`, request.ID)
	}))
	defer server.Close()
	t.Setenv("MULTICA_SERVER_URL", server.URL)

	cmd := newMCPCallCommand()
	if err := cmd.Flags().Set("method", "method_added_after_image_build"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("arguments", `{"keyword":"dynamic"}`); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return cmd.RunE(cmd, nil) })
	if err != nil {
		t.Fatalf("mcp call: %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("request count = %d, want one tools/call request and no initialize", requestCount)
	}
	if !strings.Contains(out, `"structuredContent"`) || !strings.Contains(out, `"source": "server"`) {
		t.Fatalf("mcp call output did not preserve the generic result: %s", out)
	}
}
