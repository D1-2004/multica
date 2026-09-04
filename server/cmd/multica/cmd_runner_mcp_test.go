package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunnerMCPReportPreservesUserFieldsAndRevisionTracksEffectiveConfig(t *testing.T) {
	raw := []byte(`{
		"mcpServers": {
			"wiki": {
				"command": "node",
				"args": ["/private/wiki-server.js"],
				"env": {"WIKI_TOKEN": "secret-one"}
			},
			"search": {
				"type": "http",
				"url": "http://127.0.0.1:9900/mcp",
				"headers": {"Authorization": "Bearer local-secret"}
			}
		},
		"vendorExtension": {"keep": true}
	}`)
	first, err := runnerMCPInventoryFromJSON(raw)
	if err != nil {
		t.Fatalf("parse Runner MCP config: %v", err)
	}
	if len(first.Servers) != 3 {
		t.Fatalf("inventory = %#v", first.Servers)
	}
	summaries := make(map[string]runnerMCPServerSummary, len(first.Servers))
	for _, server := range first.Servers {
		summaries[server.Name] = server
		if server.Fingerprint == "" || server.Availability != "available" {
			t.Fatalf("incomplete summary = %#v", server)
		}
	}
	if summaries["search"].Transport != "http" || summaries["wiki"].Transport != "stdio" || summaries[runnerBuiltinShellMCPName].Transport != "stdio" {
		t.Fatalf("inventory transports = %#v", summaries)
	}
	var effective struct {
		MCPServers     map[string]json.RawMessage `json:"mcpServers"`
		VendorExtension map[string]bool           `json:"vendorExtension"`
	}
	if err := json.Unmarshal(first.Config, &effective); err != nil {
		t.Fatal(err)
	}
	if !effective.VendorExtension["keep"] || len(effective.MCPServers) != 3 {
		t.Fatalf("effective config lost user fields: %s", first.Config)
	}
	wire, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip runnerMCPInventory
	if err := json.Unmarshal(wire, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(roundTrip.Config, first.Config) {
		t.Fatalf("wire report changed effective config bytes:\nwant: %q\n got: %q", first.Config, roundTrip.Config)
	}
	for _, expected := range []string{"node", "/private/wiki-server.js", "secret-one", "127.0.0.1", "local-secret", "vendorExtension"} {
		if !strings.Contains(string(first.Config), expected) {
			t.Fatalf("reported config lost %q: %s", expected, first.Config)
		}
	}

	second, err := runnerMCPInventoryFromJSON([]byte(`{
		"mcpServers": {
			"wiki": {
				"command": "node",
				"args": ["/private/wiki-server.js"],
				"env": {"WIKI_TOKEN": "secret-two"}
			}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var secondWiki runnerMCPServerSummary
	for _, server := range second.Servers {
		if server.Name == "wiki" {
			secondWiki = server
		}
	}
	if summaries["wiki"].Fingerprint == secondWiki.Fingerprint {
		t.Fatal("secret-bearing config replacement kept the same fingerprint")
	}
}

func TestRunnerMCPInventoryRejectsInvalidNamesAndTransports(t *testing.T) {
	for _, raw := range []string{
		`{"mcpServers":{"../escape":{"command":"node"}}}`,
		`{"mcpServers":{"missing":{}}}`,
		`{"mcpServers":{"ambiguous":{"command":"node","url":"http://127.0.0.1/mcp"}}}`,
	} {
		if _, err := runnerMCPInventoryFromJSON([]byte(raw)); err == nil {
			t.Fatalf("invalid Runner MCP config accepted: %s", raw)
		}
	}
}

func TestRunnerMCPReportAcceptsRemoteHTTPWithoutRewritingIt(t *testing.T) {
	raw := []byte(`{"mcpServers":{"remote":{"type":"http","url":"https://example.com/mcp","headers":{"X-Custom":"exact"}}}}`)
	report, err := runnerMCPInventoryFromJSON(raw)
	if err != nil {
		t.Fatalf("remote HTTP config rejected: %v", err)
	}
	var effective struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(report.Config, &effective); err != nil {
		t.Fatal(err)
	}
	var remote map[string]any
	if err := json.Unmarshal(effective.MCPServers["remote"], &remote); err != nil {
		t.Fatal(err)
	}
	if remote["type"] != "http" || remote["url"] != "https://example.com/mcp" || remote["headers"].(map[string]any)["X-Custom"] != "exact" {
		t.Fatalf("remote config fields were rewritten: %s", report.Config)
	}
}

func TestRunnerMCPBuiltinAlwaysReported(t *testing.T) {
	report, err := runnerMCPInventoryFromJSON([]byte(`{"mcpServers":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Servers) != 1 {
		t.Fatalf("servers = %#v, want only built-in Shell MCP", report.Servers)
	}
	server := report.Servers[0]
	if server.Name != "multica_runner" || server.Transport != "stdio" || server.Availability != "available" || server.Fingerprint == "" {
		t.Fatalf("built-in summary = %#v", server)
	}
	var effective struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(report.Config, &effective); err != nil {
		t.Fatal(err)
	}
	if _, ok := effective.MCPServers["multica_runner"]; !ok {
		t.Fatalf("reported config does not contain built-in Shell MCP: %s", report.Config)
	}
	var builtIn map[string]string
	if err := json.Unmarshal(effective.MCPServers["multica_runner"], &builtIn); err != nil {
		t.Fatal(err)
	}
	if builtIn["type"] != "builtin" || builtIn["builtin"] != "shell" {
		t.Fatalf("built-in Shell MCP descriptor = %#v", builtIn)
	}
	if _, hasCommand := builtIn["command"]; hasCommand {
		t.Fatalf("built-in Shell MCP must not advertise a child-process command: %#v", builtIn)
	}
}

func TestRunnerMCPBuiltinNameIsReserved(t *testing.T) {
	_, err := runnerMCPInventoryFromJSON([]byte(`{"mcpServers":{"multica_runner":{"command":"custom"}}}`))
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved built-in name error = %v", err)
	}
}
