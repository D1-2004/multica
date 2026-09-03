package handler

import (
	"testing"

	"github.com/multica-ai/multica/server/pkg/runnerprotocol"
)

func TestResolveRunnerMCPServersKeepsSnapshotWhenInventoryIsTemporarilyMissing(t *testing.T) {
	enabled := map[string]string{"llm-wiki": "sha256:configured"}

	resolved := resolveRunnerMCPServers(enabled, runnerprotocol.MCPInventory{}, false)

	if resolved["llm-wiki"] != "sha256:configured" {
		t.Fatalf("resolved servers = %#v, want persisted llm-wiki selection", resolved)
	}
}

func TestResolveRunnerMCPServersUsesEveryAvailableLiveServer(t *testing.T) {
	enabled := map[string]string{"llm-wiki": "sha256:configured"}
	inventory := runnerprotocol.MCPInventory{Servers: []runnerprotocol.MCPServerSummary{
		{Name: "llm-wiki", Availability: "available", Fingerprint: "sha256:changed"},
		{Name: "future-mcp", Availability: "available", Fingerprint: "sha256:future"},
		{Name: "offline-mcp", Availability: "unavailable", Fingerprint: "sha256:offline"},
	}}

	resolved := resolveRunnerMCPServers(enabled, inventory, true)

	if len(resolved) != 2 || resolved["llm-wiki"] != "sha256:changed" || resolved["future-mcp"] != "sha256:future" {
		t.Fatalf("resolved servers = %#v, want every available live server", resolved)
	}
}
