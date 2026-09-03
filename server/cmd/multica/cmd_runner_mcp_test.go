package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRunnerMCPInventoryIsRedactedAndFingerprintTracksConfig(t *testing.T) {
	first, err := runnerMCPInventoryFromJSON([]byte(`{
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
		}
	}`))
	if err != nil {
		t.Fatalf("parse Runner MCP config: %v", err)
	}
	if len(first.Servers) != 2 || first.Servers[0].Name != "search" || first.Servers[0].Transport != "http" || first.Servers[1].Name != "wiki" || first.Servers[1].Transport != "stdio" {
		t.Fatalf("inventory = %#v", first.Servers)
	}
	for _, server := range first.Servers {
		if server.Fingerprint == "" || server.Availability != "available" {
			t.Fatalf("incomplete summary = %#v", server)
		}
	}
	reported, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"node", "/private/wiki-server.js", "secret-one", "127.0.0.1", "local-secret"} {
		if strings.Contains(string(reported), secret) {
			t.Fatalf("inventory leaked %q: %s", secret, reported)
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
	if first.Servers[1].Fingerprint == second.Servers[0].Fingerprint {
		t.Fatal("secret-bearing config replacement kept the same fingerprint")
	}
}

func TestRunnerMCPInventoryRejectsInvalidNamesAndTransports(t *testing.T) {
	for _, raw := range []string{
		`{"mcpServers":{"../escape":{"command":"node"}}}`,
		`{"mcpServers":{"missing":{}}}`,
		`{"mcpServers":{"ambiguous":{"command":"node","url":"http://127.0.0.1/mcp"}}}`,
		`{"mcpServers":{"remote":{"url":"https://example.com/mcp"}}}`,
	} {
		if _, err := runnerMCPInventoryFromJSON([]byte(raw)); err == nil {
			t.Fatalf("invalid Runner MCP config accepted: %s", raw)
		}
	}
}
