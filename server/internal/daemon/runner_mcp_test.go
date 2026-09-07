package daemon

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/pkg/runnerprotocol"
)

func TestRebaseManagedRunnerMCPUsesDaemonServerOrigin(t *testing.T) {
	raw := json.RawMessage(`{"mcpServers":{"custom":{"url":"https://custom.example/mcp"},"multica_runner":{"type":"http","url":"https://pre.example/api/runner-mcp","headers":{"Authorization":"Bearer mat_task","X-Multica-Runner-MCP":"v1"}}},"other":{"keep":true}}`)

	got, err := rebaseManagedRunnerMCP(raw, "http://127.0.0.1:39123")
	if err != nil {
		t.Fatalf("rebase managed Runner MCP: %v", err)
	}
	var document struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
		Other map[string]bool `json:"other"`
	}
	if err := json.Unmarshal(got, &document); err != nil {
		t.Fatalf("parse rebased config: %v", err)
	}
	runner := document.MCPServers[runnerprotocol.ManagedMCPServerName]
	if runner.URL != "http://127.0.0.1:39123/api/runner-mcp" {
		t.Fatalf("Runner URL = %q", runner.URL)
	}
	if runner.Headers["Authorization"] != "Bearer mat_task" {
		t.Fatalf("Runner authorization was not preserved: %#v", runner.Headers)
	}
	if _, ok := runner.Headers[runnerprotocol.ManagedMCPRoutingHeader]; ok {
		t.Fatalf("internal routing marker reached the child config: %#v", runner.Headers)
	}
	if document.MCPServers["custom"].URL != "https://custom.example/mcp" || !document.Other["keep"] {
		t.Fatalf("unrelated MCP configuration changed: %s", got)
	}
}

func TestRebaseManagedRunnerMCPLeavesUnmanagedEntryUnchanged(t *testing.T) {
	raw := json.RawMessage(`{"mcpServers":{"multica_runner":{"url":"https://user.example/mcp","headers":{"Authorization":"Bearer user-token"}}}}`)
	got, err := rebaseManagedRunnerMCP(raw, "http://127.0.0.1:39123")
	if err != nil {
		t.Fatalf("rebase unmanaged Runner MCP: %v", err)
	}
	if string(got) != string(raw) {
		t.Fatalf("unmanaged config changed: %s", got)
	}
}

func TestRebaseManagedRunnerMCPRejectsInvalidDaemonURL(t *testing.T) {
	raw := json.RawMessage(`{"mcpServers":{"multica_runner":{"url":"https://pre.example/api/runner-mcp","headers":{"X-Multica-Runner-MCP":"v1"}}}}`)
	if _, err := rebaseManagedRunnerMCP(raw, "file:///tmp/multica"); err == nil {
		t.Fatal("invalid daemon URL was accepted")
	}
}

func TestRebaseManagedRunnerMCPRewritesEveryMarkedMountAndKeepsPath(t *testing.T) {
	raw := json.RawMessage(`{"mcpServers":{"filesystem":{"url":"https://pre.example/api/runner-mcp/mounts/111/servers/filesystem","headers":{"Authorization":"Bearer mat_task","X-Multica-Runner-MCP":"v1"}},"browser":{"url":"https://pre.example/api/runner-mcp/mounts/111/servers/browser","headers":{"X-Multica-Runner-MCP":"v1"}}}}`)
	got, err := rebaseManagedRunnerMCP(raw, "http://127.0.0.1:39123")
	if err != nil {
		t.Fatal(err)
	}
	var document struct{ MCPServers map[string]struct{ URL string `json:"url"`; Headers map[string]string `json:"headers"` } `json:"mcpServers"` }
	if err := json.Unmarshal(got, &document); err != nil {
		t.Fatal(err)
	}
	if document.MCPServers["filesystem"].URL != "http://127.0.0.1:39123/api/runner-mcp/mounts/111/servers/filesystem" {
		t.Fatalf("filesystem URL = %q", document.MCPServers["filesystem"].URL)
	}
	if document.MCPServers["browser"].URL != "http://127.0.0.1:39123/api/runner-mcp/mounts/111/servers/browser" {
		t.Fatalf("browser URL = %q", document.MCPServers["browser"].URL)
	}
}

func TestRebaseManagedMCPUsesRouteMapAndPreservesUnroutedEntries(t *testing.T) {
	raw := json.RawMessage(`{"mcpServers":{"wiki":{"command":"node","args":["wiki.js"],"env":{"TOKEN":"secret"},"vendor":{"keep":true}},"direct":{"url":"https://direct.example/mcp","headers":{"X-Direct":"yes"}}}}`)
	routes := map[string]mcpRelayRoute{
		"wiki": {Path: "/api/runner-mcp/mounts/binding/servers/wiki", Authorization: "Bearer task-token"},
	}

	got, err := rebaseManagedMCP(raw, routes, "http://127.0.0.1:39123")
	if err != nil {
		t.Fatalf("rebase managed MCP: %v", err)
	}
	var document struct {
		MCPServers map[string]map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(got, &document); err != nil {
		t.Fatal(err)
	}
	wiki := document.MCPServers["wiki"]
	if string(wiki["url"]) != `"http://127.0.0.1:39123/api/runner-mcp/mounts/binding/servers/wiki"` {
		t.Fatalf("wiki relay URL = %s", wiki["url"])
	}
	if _, exists := wiki["command"]; exists {
		t.Fatal("Runner-local command reached runtime-facing relay config")
	}
	if string(wiki["vendor"]) != `{"keep":true}` {
		t.Fatalf("non-transport field changed: %s", wiki["vendor"])
	}
	if string(document.MCPServers["direct"]["url"]) != `"https://direct.example/mcp"` {
		t.Fatalf("direct MCP was changed: %s", got)
	}
}
