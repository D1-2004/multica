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
