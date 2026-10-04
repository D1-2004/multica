package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

const connectorDiscoveryStatusTool = "multica_connection_unavailable"

// Classify errors without propagating URLs, credentials or upstream bodies.
func connectorFailureClass(err error) (string, int) {
	switch {
	case err == nil:
		return "", 0
	case errors.Is(err, context.Canceled):
		return "request_cancelled", 0
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", 0
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

// Only an unavailable first page can become an explicit local diagnostic.
// Invalid metadata, unknown failures and incomplete pagination stay errors.
func connectorUnavailableDiscovery(ctx context.Context, c internalConnector, cursor string, err error) (any, bool) {
	if err == nil || cursor != "" || ctx.Err() != nil {
		return nil, false
	}
	class, status := connectorFailureClass(err)
	switch class {
	case "timeout", "network", "upstream_http", "reconnect_required", "credential_refresh_busy", "credential_refresh_backoff":
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
		"do not claim success or substitute local credentials. Start a new run to retry discovery; reconnect only if the connection requires it."
	return map[string]any{"tools": []map[string]any{{
		"name": connectorDiscoveryStatusTool, "description": description,
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "openWorldHint": false},
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
		"message": "Business tools were unavailable when this diagnostic was advertised. No business operation was performed. " +
			"This diagnostic does not check current upstream health. If this connector is required, report the work blocked and retry discovery in a new run. " +
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
