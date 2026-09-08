package mcpprotocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMergeManagedConfigPreservesEveryField(t *testing.T) {
	base := json.RawMessage(`{"mcpServers":{"direct":{"url":"https://agent.example/mcp","headers":{"X-Agent":"keep"}}}}`)
	managed := json.RawMessage(`{"mcpServers":{"wiki":{"command":"node","args":["wiki.js"],"env":{"TOKEN":"secret"},"vendor":{"keep":true}}},"extension":{"raw":true}}`)
	got, names, err := MergeManagedConfig(base, managed)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "wiki" {
		t.Fatalf("managed names = %#v", names)
	}
	for _, expected := range []string{`"TOKEN":"secret"`, `"vendor":{"keep":true}`, `"extension":{"raw":true}`, `"X-Agent":"keep"`} {
		if !strings.Contains(string(got), expected) {
			t.Fatalf("merged config lost %s: %s", expected, got)
		}
	}
}

func TestMergeManagedConfigRejectsServerNameCollision(t *testing.T) {
	base := json.RawMessage(`{"mcpServers":{"wiki":{"url":"https://agent.example/mcp"}}}`)
	managed := json.RawMessage(`{"mcpServers":{"wiki":{"command":"node"}}}`)
	if _, _, err := MergeManagedConfig(base, managed); err == nil || !strings.Contains(err.Error(), "mcp_server_name_conflict") {
		t.Fatalf("collision error = %v", err)
	}
}
