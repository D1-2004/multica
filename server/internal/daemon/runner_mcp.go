package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/multica-ai/multica/server/pkg/runnerprotocol"
)

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
	runnerRaw, ok := servers[runnerprotocol.ManagedMCPServerName]
	if !ok {
		return raw, nil
	}

	var runner map[string]json.RawMessage
	if err := json.Unmarshal(runnerRaw, &runner); err != nil {
		return nil, fmt.Errorf("parse managed Runner MCP entry: %w", err)
	}
	headersRaw, ok := runner["headers"]
	if !ok {
		return raw, nil
	}
	var headers map[string]string
	if err := json.Unmarshal(headersRaw, &headers); err != nil {
		return nil, fmt.Errorf("parse managed Runner MCP headers: %w", err)
	}
	markerKey, marked := runnerMCPRoutingMarker(headers)
	if !marked {
		return raw, nil
	}
	delete(headers, markerKey)

	endpoint, err := managedRunnerMCPEndpoint(serverBaseURL)
	if err != nil {
		return nil, err
	}
	urlRaw, err := json.Marshal(endpoint)
	if err != nil {
		return nil, fmt.Errorf("marshal managed Runner MCP URL: %w", err)
	}
	headersRaw, err = json.Marshal(headers)
	if err != nil {
		return nil, fmt.Errorf("marshal managed Runner MCP headers: %w", err)
	}
	runner["url"] = urlRaw
	runner["headers"] = headersRaw
	runnerRaw, err = json.Marshal(runner)
	if err != nil {
		return nil, fmt.Errorf("marshal managed Runner MCP entry: %w", err)
	}
	servers[runnerprotocol.ManagedMCPServerName] = runnerRaw
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

func managedRunnerMCPEndpoint(serverBaseURL string) (string, error) {
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
	parsed.Path = strings.TrimRight(parsed.Path, "/") + runnerprotocol.ManagedMCPPath
	parsed.RawPath = ""
	return parsed.String(), nil
}
