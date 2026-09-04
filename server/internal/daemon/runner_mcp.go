package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/multica-ai/multica/server/pkg/mcpprotocol"
	"github.com/multica-ai/multica/server/pkg/runnerprotocol"
)

type mcpRelayRoute = mcpprotocol.RelayRoute

// rebaseManagedMCP builds the runtime-facing projection of an unchanged task
// MCP document. Only server names explicitly present in routes are converted
// to the sandbox loopback relay; configuration content is never inspected to
// infer routing.
func rebaseManagedMCP(raw json.RawMessage, routes map[string]mcpRelayRoute, serverBaseURL string) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(routes) == 0 || len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return raw, nil
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &document); err != nil {
		return nil, fmt.Errorf("parse mcp_config for managed routing: %w", err)
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(document["mcpServers"], &servers); err != nil {
		return nil, fmt.Errorf("parse mcpServers for managed routing: %w", err)
	}
	changed := false
	for name, route := range routes {
		serverRaw, ok := servers[name]
		if !ok {
			return nil, fmt.Errorf("managed MCP route %q has no matching config entry", name)
		}
		var server map[string]json.RawMessage
		if err := json.Unmarshal(serverRaw, &server); err != nil {
			return nil, fmt.Errorf("parse MCP entry %q for managed routing: %w", name, err)
		}
		endpoint, err := managedRunnerMCPEndpoint(serverBaseURL, route.Path)
		if err != nil {
			return nil, fmt.Errorf("route MCP entry %q: %w", name, err)
		}
		for _, field := range []string{"command", "args", "env", "url", "headers", "type"} {
			delete(server, field)
		}
		server["type"], _ = json.Marshal("http")
		server["url"], _ = json.Marshal(endpoint)
		if route.Authorization != "" {
			server["headers"], _ = json.Marshal(map[string]string{"Authorization": route.Authorization})
		}
		servers[name], err = json.Marshal(server)
		if err != nil {
			return nil, fmt.Errorf("marshal MCP entry %q after managed routing: %w", name, err)
		}
		changed = true
	}
	if !changed {
		return raw, nil
	}
	serversRaw, err := json.Marshal(servers)
	if err != nil {
		return nil, fmt.Errorf("marshal mcpServers after managed routing: %w", err)
	}
	document["mcpServers"] = serversRaw
	result, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("marshal mcp_config after managed routing: %w", err)
	}
	return result, nil
}

// rebaseManagedRunnerMCP keeps the product-managed Runner endpoint on the
// same Multica origin already selected by the daemon. In pre-release FC/E2B
// sandboxes that origin is the per-task loopback egress relay; elsewhere it is
// the daemon's ordinary server origin.
func rebaseManagedRunnerMCP(raw json.RawMessage, serverBaseURL string) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return raw, nil
	}

	var document map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &document); err != nil {
		return nil, fmt.Errorf("parse mcp_config for Runner routing: %w", err)
	}
	serversRaw, ok := document["mcpServers"]
	if !ok {
		return raw, nil
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(serversRaw, &servers); err != nil {
		return nil, fmt.Errorf("parse mcpServers for Runner routing: %w", err)
	}
	changed := false
	for name, serverRaw := range servers {
		var server map[string]json.RawMessage
		if err := json.Unmarshal(serverRaw, &server); err != nil {
			return nil, fmt.Errorf("parse MCP entry %q for Runner routing: %w", name, err)
		}
		headersRaw, ok := server["headers"]
		if !ok {
			continue
		}
		var headers map[string]string
		if err := json.Unmarshal(headersRaw, &headers); err != nil {
			return nil, fmt.Errorf("parse MCP headers %q for Runner routing: %w", name, err)
		}
		markerKey, marked := runnerMCPRoutingMarker(headers)
		if !marked {
			continue
		}
		var originalURL string
		if json.Unmarshal(server["url"], &originalURL) != nil {
			return nil, fmt.Errorf("parse MCP URL %q for Runner routing", name)
		}
		endpoint, err := managedRunnerMCPEndpoint(serverBaseURL, originalURL)
		if err != nil {
			return nil, err
		}
		delete(headers, markerKey)
		server["url"], _ = json.Marshal(endpoint)
		server["headers"], _ = json.Marshal(headers)
		servers[name], err = json.Marshal(server)
		if err != nil {
			return nil, fmt.Errorf("marshal MCP entry %q after Runner routing: %w", name, err)
		}
		changed = true
	}
	if !changed {
		return raw, nil
	}
	var err error
	serversRaw, err = json.Marshal(servers)
	if err != nil {
		return nil, fmt.Errorf("marshal mcpServers after Runner routing: %w", err)
	}
	document["mcpServers"] = serversRaw
	result, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("marshal mcp_config after Runner routing: %w", err)
	}
	return result, nil
}

func runnerMCPRoutingMarker(headers map[string]string) (string, bool) {
	for key, value := range headers {
		if strings.EqualFold(key, runnerprotocol.ManagedMCPRoutingHeader) {
			return key, value == runnerprotocol.ManagedMCPRoutingValue
		}
	}
	return "", false
}

func managedRunnerMCPEndpoint(serverBaseURL, originalURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(serverBaseURL))
	if err != nil {
		return "", fmt.Errorf("parse daemon server URL for Runner MCP: %w", err)
	}
	if parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("daemon server URL for Runner MCP must be an HTTP(S) origin")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("daemon server URL for Runner MCP must not contain a query or fragment")
	}
	original, err := url.Parse(strings.TrimSpace(originalURL))
	if err != nil || original.Path == "" || original.RawQuery != "" || original.Fragment != "" {
		return "", errors.New("managed Runner MCP URL must contain a path without query or fragment")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + original.Path
	parsed.RawPath = ""
	return parsed.String(), nil
}
