package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

const (
	connectorDiscoveryStatusTool = "multica_connection_unavailable"
	connectorRecoverTool         = "multica_recover_connection"
	connectorRecoveredCallTool   = "multica_call_recovered_tool"
)

type connectorControlCollisionError struct{}

func (connectorControlCollisionError) Error() string { return "reserved connector tool name" }

func connectorControlTool(name string) bool {
	return name == connectorDiscoveryStatusTool || name == connectorRecoverTool || name == connectorRecoveredCallTool
}

// Classify errors without propagating URLs, credentials or upstream bodies.
func connectorFailureClass(err error) (string, int) {
	switch {
	case err == nil:
		return "", 0
	case errors.Is(err, context.Canceled):
		return "request_cancelled", 0
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", 0
	case errors.Is(err, errConnectorRecoveryLimit):
		return "catalog_limit", 0
	case errors.Is(err, errConnectorReconnectRequired):
		return "reconnect_required", 0
	case errors.Is(err, errConnectorRefreshBusy):
		return "credential_refresh_busy", 0
	case errors.Is(err, errConnectorRefreshBackoff):
		return "credential_refresh_backoff", 0
	}
	var status connectorUpstreamStatusError
	if errors.As(err, &status) {
		return "upstream_http", status.Code
	}
	var remoteStatus *remotemcp.StatusError
	if errors.As(err, &remoteStatus) {
		return "upstream_http", remoteStatus.StatusCode
	}
	var protocol connectorUpstreamProtocolError
	var collision connectorControlCollisionError
	if errors.As(err, &collision) {
		return "reserved_tool_collision", 0
	}
	var rpc *remotemcp.RPCError
	if errors.As(err, &protocol) || errors.As(err, &rpc) || errors.Is(err, remotemcp.ErrInvalidResponse) {
		return "invalid_response", 0
	}
	var network net.Error
	if errors.As(err, &network) {
		if network.Timeout() {
			return "timeout", 0
		}
		return "network", 0
	}
	return "upstream_failure", 0
}

// Known upstream failures expose recovery controls, never invalid definitions.
// Clients may retain validated earlier pages; diagnostics mark incompleteness.
func connectorUnavailableDiscovery(ctx context.Context, c internalConnector, cursor string, err error) (any, bool) {
	if err == nil || ctx.Err() != nil {
		return nil, false
	}
	class, status := connectorFailureClass(err)
	switch class {
	case "timeout", "network", "upstream_http", "reconnect_required", "credential_refresh_busy", "credential_refresh_backoff", "invalid_response", "reserved_tool_collision":
	default:
		return nil, false
	}
	reason := class
	if status != 0 {
		reason = fmt.Sprintf("%s (HTTP %d)", class, status)
	}
	description := fmt.Sprintf("Business tools for connector %q were unavailable during this run's discovery: ", c.Name) + reason +
		". This read-only diagnostic cannot perform business work or confirm recovery. " +
		"Use other authorized connectors for unrelated work. If the requested work requires this connector, report it blocked; " +
		"do not claim success or substitute local credentials. Use multica_recover_connection for one explicit read-only recovery attempt in this Run. " +
		"If recovery succeeds, use multica_call_recovered_tool to explicitly call a returned tool. Reconnect authorization only when required; do not replay completed business actions."
	if cursor != "" {
		description = "Complete catalog discovery failed. Verified tools from earlier pages may remain available, but the catalog is incomplete; missing tools do not imply denied permission. " + description
	}
	return map[string]any{"_meta": map[string]any{"multica_catalog_complete": false}, "tools": []map[string]any{{
		"name": connectorDiscoveryStatusTool, "description": description,
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false},
	}, {
		"name":        connectorRecoverTool,
		"description": "Explicitly reconnect and discover this connector's complete live authorized tool catalog in the current Run. Read-only, bounded, and does not execute or replay business work. Failure returns a safe status; do not repeatedly retry authentication or invalid-metadata errors. After success use multica_call_recovered_tool with an exact returned tool_name and its schema. Only actual business tool results prove business completion.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": true},
	}, {
		"name":        connectorRecoveredCallTool,
		"description": "Explicitly execute a tool from multica_recover_connection's returned catalog, using its exact tool_name and input schema. Current task/connector/account/write permissions are rechecked. This performs one business call and never automatically retries or replays completed work. Do not use alternate credentials or infer success from reconnect alone.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"tool_name": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object"},
		}, "required": []string{"tool_name", "arguments"}, "additionalProperties": false},
		"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "openWorldHint": true},
	}}}, true
}

func connectorDiscoveryStatusResult(c internalConnector, arguments json.RawMessage) (multicaMCPToolResult, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(arguments, &object) != nil || object == nil || len(object) != 0 {
		return multicaMCPToolResult{}, false
	}
	status := map[string]any{
		"connector_id": c.ID, "connector_name": c.Name,
		"status": "discovery_unavailable", "business_execution": false, "recovery_verified": false,
		"catalog_complete": false,
		"message": "Business tools were unavailable when this diagnostic was advertised. No business operation was performed. " +
			"This diagnostic does not check current upstream health. If this connector is required, use multica_recover_connection for an explicit read-only recovery attempt in this Run. " +
			"Do not substitute local credentials or claim the requested work succeeded.",
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		return multicaMCPToolResult{}, false
	}
	return multicaMCPToolResult{
		Content: []multicaMCPContent{{Type: "text", Text: string(encoded)}}, StructuredContent: status,
	}, true
}
