package handler

import (
	"testing"

	"github.com/multica-ai/multica/server/pkg/runnerprotocol"
)

func TestResolveEnabledRunnerMCPServersKeepsSelectionWhenInventoryIsTemporarilyMissing(t *testing.T) {
	enabled := map[string]string{"llm-wiki": "sha256:configured"}

	resolved := resolveEnabledRunnerMCPServers(enabled, runnerprotocol.MCPInventory{}, false)

	if resolved["llm-wiki"] != "sha256:configured" {
		t.Fatalf("resolved servers = %#v, want persisted llm-wiki selection", resolved)
	}
}

func TestResolveEnabledRunnerMCPServersRejectsChangedLiveFingerprint(t *testing.T) {
	enabled := map[string]string{"llm-wiki": "sha256:configured"}
	inventory := runnerprotocol.MCPInventory{Servers: []runnerprotocol.MCPServerSummary{{
		Name: "llm-wiki", Availability: "available", Fingerprint: "sha256:changed",
	}}}

	resolved := resolveEnabledRunnerMCPServers(enabled, inventory, true)

	if len(resolved) != 0 {
		t.Fatalf("resolved servers = %#v, want changed live server rejected", resolved)
	}
}
