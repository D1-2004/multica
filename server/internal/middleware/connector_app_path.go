package middleware

import "strings"

// IsConnectorAppConfigPath reports whether path is the workspace connector
// application admin API. A workspace MCP token may call this path in
// addition to its MCP endpoint; every other route stays closed.
func IsConnectorAppConfigPath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 4 || parts[0] != "api" || parts[1] != "workspaces" || parts[3] != "connector-apps" {
		return false
	}
	return parts[2] != "" && !strings.Contains(path, "..")
}
