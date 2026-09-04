package mcpprotocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

type RelayRoute struct {
	Path          string `json:"path"`
	Authorization string `json:"authorization,omitempty"`
}

func MergeManagedConfig(base, managed json.RawMessage) (json.RawMessage, []string, error) {
	document := make(map[string]json.RawMessage)
	if trimmed := bytes.TrimSpace(base); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if err := json.Unmarshal(trimmed, &document); err != nil {
			return nil, nil, fmt.Errorf("parse base MCP config: %w", err)
		}
	}
	var managedDocument map[string]json.RawMessage
	if err := json.Unmarshal(managed, &managedDocument); err != nil {
		return nil, nil, fmt.Errorf("parse managed MCP config: %w", err)
	}
	baseServers, err := serverMap(document["mcpServers"])
	if err != nil {
		return nil, nil, fmt.Errorf("parse base MCP servers: %w", err)
	}
	managedServers, err := serverMap(managedDocument["mcpServers"])
	if err != nil {
		return nil, nil, fmt.Errorf("parse managed MCP servers: %w", err)
	}
	names := make([]string, 0, len(managedServers))
	for name, entry := range managedServers {
		if _, exists := baseServers[name]; exists {
			return nil, nil, fmt.Errorf("mcp_server_name_conflict: %s", name)
		}
		baseServers[name] = append(json.RawMessage(nil), entry...)
		names = append(names, name)
	}
	for key, value := range managedDocument {
		if key == "mcpServers" {
			continue
		}
		if existing, exists := document[key]; exists && !bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(value)) {
			return nil, nil, fmt.Errorf("mcp_config_field_conflict: %s", key)
		}
		document[key] = append(json.RawMessage(nil), value...)
	}
	serversRaw, err := json.Marshal(baseServers)
	if err != nil {
		return nil, nil, err
	}
	document["mcpServers"] = serversRaw
	merged, err := json.Marshal(document)
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(names)
	return merged, names, nil
}

func serverMap(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return make(map[string]json.RawMessage), nil
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(raw, &servers); err != nil {
		return nil, err
	}
	if servers == nil {
		servers = make(map[string]json.RawMessage)
	}
	return servers, nil
}
