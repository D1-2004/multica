package middleware

import "testing"

func TestIsConnectorAppConfigPath(t *testing.T) {
	allowed := []string{
		"/api/workspaces/11111111-1111-1111-1111-111111111111/connector-apps",
		"/api/workspaces/11111111-1111-1111-1111-111111111111/connector-apps/resolve",
		"/api/workspaces/ws/connector-apps/app/instances/inst/bindings",
	}
	for _, path := range allowed {
		if !IsConnectorAppConfigPath(path) {
			t.Fatalf("expected %s to be allowed", path)
		}
	}
	denied := []string{
		"/api/mcp/workspaces/ws",
		"/api/workspaces/ws/internal-connectors",
		"/api/workspaces/ws/connector-apps-extra",
		"/api/workspaces//connector-apps",
		"/api/workspaces/ws/connector-apps/../../tokens",
	}
	for _, path := range denied {
		if IsConnectorAppConfigPath(path) {
			t.Fatalf("expected %s to be denied", path)
		}
	}
}
