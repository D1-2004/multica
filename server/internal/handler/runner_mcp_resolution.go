package handler

import "github.com/multica-ai/multica/server/pkg/runnerprotocol"

// resolveEnabledRunnerMCPServers treats the persisted Agent selection as the
// fallback authority while the non-secret inventory cache is temporarily
// unavailable. A live inventory remains authoritative when present, so a
// changed fingerprint or unavailable server is still rejected before routing.
// The Runner repeats the fingerprint check when executing every forwarded
// request, keeping this fallback fail-closed if the local configuration changed.
func resolveEnabledRunnerMCPServers(enabled map[string]string, inventory runnerprotocol.MCPInventory, inventoryOK bool) map[string]string {
	resolved := make(map[string]string, len(enabled))
	if !inventoryOK {
		for name, fingerprint := range enabled {
			resolved[name] = fingerprint
		}
		return resolved
	}
	for name, fingerprint := range enabled {
		for _, summary := range inventory.Servers {
			if summary.Name == name && summary.Availability == "available" && summary.Fingerprint == fingerprint {
				resolved[name] = fingerprint
				break
			}
		}
	}
	return resolved
}
