package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunnerMCPReportPreservesRawConfigAndRevisionTracksBytes(t *testing.T) {
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
	if len(first.Servers) != 2 || first.Servers[0].Name != "search" || first.Servers[0].Transport != "http" || first.Servers[1].Name != "wiki" || first.Servers[1].Transport != "stdio" {
		t.Fatalf("inventory = %#v", first.Servers)
	}
	for _, server := range first.Servers {
		if server.Fingerprint == "" || server.Availability != "available" {
			t.Fatalf("incomplete summary = %#v", server)
		}
	}
	if string(first.Config) != string(raw) {
		t.Fatalf("reported config was rewritten:\nwant: %s\n got: %s", raw, first.Config)
	}
	wire, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip runnerMCPInventory
	if err := json.Unmarshal(wire, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(roundTrip.Config, raw) {
		t.Fatalf("wire report changed raw config bytes:\nwant: %q\n got: %q", raw, roundTrip.Config)
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
	if first.Servers[1].Fingerprint == second.Servers[0].Fingerprint {
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
	if string(report.Config) != string(raw) {
		t.Fatalf("remote config was rewritten: %s", report.Config)
	}
}
