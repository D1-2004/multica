package handler

import "github.com/multica-ai/multica/server/pkg/runnerprotocol"

// resolveRunnerMCPServers exposes every available server in a live
// Runner inventory. The persisted map is only a last-known fallback while the
// non-secret inventory cache is temporarily unavailable; it is not a per-Agent
// allowlist. The Runner repeats the fingerprint check on every forwarded
// request, keeping the fallback fail-closed if local configuration changed.
func resolveRunnerMCPServers(snapshot map[string]string, inventory runnerprotocol.MCPInventory, inventoryOK bool) map[string]string {
	if !inventoryOK {
		resolved := make(map[string]string, len(snapshot))
		for name, fingerprint := range snapshot {
			resolved[name] = fingerprint
		}
		return resolved
	}
	resolved := make(map[string]string, len(inventory.Servers))
	for _, summary := range inventory.Servers {
		if summary.Name != "" && summary.Availability == "available" && summary.Fingerprint != "" {
			resolved[summary.Name] = summary.Fingerprint
		}
	}
	return resolved
}
