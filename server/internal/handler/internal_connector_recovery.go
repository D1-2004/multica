package handler

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const (
	connectorTaskDiscoveryTimeout = 10 * time.Second
	connectorRecoveryTimeout      = 30 * time.Second
	connectorRecoveryMaxPages     = 16
	connectorRecoveryMaxBytes     = 2 << 20
)

var errConnectorRecoveryLimit = errors.New("complete connector catalog exceeds recovery limits")

type connectorDiscoveryWaitKey struct{}

// A recovery call unwraps into the existing native business-call path. It
// cannot choose transport, URL, identity or a control tool recursively.
func connectorRecoveredParams(raw json.RawMessage) (connectorRPCParams, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 2 {
		return connectorRPCParams{}, false
	}
	var name string
	if json.Unmarshal(fields["tool_name"], &name) != nil || name == "" || connectorControlTool(name) || !internalMCPJSONObject(fields["arguments"]) {
		return connectorRPCParams{}, false
	}
	return connectorRPCParams{Name: name, Arguments: fields["arguments"]}, true
}

func (h *Handler) callConnectorDiscovery(ctx context.Context, c internalConnector, params connectorRPCParams, budget time.Duration) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	ctx = context.WithValue(ctx, connectorDiscoveryWaitKey{}, true)
	return h.callInternalConnectorUpstream(ctx, c, "tools/list", params)
}

func (h *Handler) recoverConnectorTools(ctx context.Context, c internalConnector, task, agent, ws string) (multicaMCPToolResult, error) {
	ctx, cancel := context.WithTimeout(ctx, connectorRecoveryTimeout)
	defer cancel()
	ctx = context.WithValue(ctx, connectorDiscoveryWaitKey{}, true)
	tools, err := h.connectorRecoveryCatalog(ctx, c, task, agent, ws)
	status := map[string]any{
		"connector_id": c.ID, "connector_name": c.Name, "business_execution": false,
		"status": "unavailable", "catalog_complete": false,
	}
	if err == nil {
		status["status"], status["catalog_complete"], status["tools"] = "available", true, tools
		status["call_tool"] = connectorRecoveredCallTool
		status["message"] = "Use call_tool with an exact returned tool_name and arguments matching its inputSchema. No business action has been performed; do not replay completed actions."
	} else {
		class, httpStatus := connectorFailureClass(err)
		status["failure_class"], status["upstream_status"] = class, httpStatus
		status["requires_reconnect"] = errors.Is(err, errConnectorReconnectRequired) || httpStatus == 401
		status["requires_permission_review"] = httpStatus == 403
		status["message"] = "Discovery remains unavailable; no business action was performed. Reconnect authorization only when required; fix invalid metadata before retrying. Unrelated authorized tools remain usable."
	}
	result, encodeErr := connectorRecoveryStatus(status)
	if encodeErr != nil {
		err = encodeErr
		// The final wire includes both text and structured content. Refuse an
		// oversized complete response instead of truncating business schemas.
		status = map[string]any{"connector_id": c.ID, "status": "unavailable", "business_execution": false,
			"catalog_complete": false, "failure_class": "catalog_limit", "message": "The complete catalog exceeds the recovery response limit; no business tools or actions were returned."}
		result, encodeErr = connectorRecoveryStatus(status)
		if encodeErr != nil {
			return multicaMCPToolResult{}, encodeErr
		}
	}
	return result, err
}

func connectorRecoveryStatus(status map[string]any) (multicaMCPToolResult, error) {
	text, err := json.Marshal(status)
	if err != nil {
		return multicaMCPToolResult{}, err
	}
	result := multicaMCPToolResult{Content: []multicaMCPContent{{Type: "text", Text: string(text)}}, StructuredContent: status}
	encoded, err := json.Marshal(result)
	if err != nil {
		return multicaMCPToolResult{}, err
	}
	if len(encoded) > connectorRecoveryMaxBytes-256 {
		return multicaMCPToolResult{}, errConnectorRecoveryLimit
	}
	return result, nil
}

func (h *Handler) connectorRecoveryCatalog(ctx context.Context, c internalConnector, task, agent, ws string) ([]map[string]any, error) {
	tools := []map[string]any{}
	cursor := ""
	seenCursors, seenNames := map[string]bool{}, map[string]bool{}
	bytes := 0
	for page := 0; page < connectorRecoveryMaxPages; page++ {
		if page > 0 {
			if err := h.connectorLimit(ctx, c.ID, task, agent, ws); err != nil {
				return nil, err
			}
		}
		var result any
		var err error
		if c.CatalogSlug == "" {
			// Original names are arguments to the fixed recovery-call tool, not
			// registered Pi names. No second metadata lookup is needed to call.
			result, err = h.callInternalConnectorUpstreamRaw(ctx, c, "tools/list", connectorRPCParams{Cursor: cursor})
		} else {
			result, err = h.callInternalConnectorUpstream(ctx, c, "tools/list", connectorRPCParams{Cursor: cursor})
		}
		if err != nil {
			return nil, err
		}
		list := result.(map[string]any)
		for _, tool := range list["tools"].([]map[string]any) {
			name, _ := tool["name"].(string)
			presented := connectorPresentedToolName(name)
			if _, ok := tool["inputSchema"].(map[string]any); !ok || !connectorValidPresentedToolName(presented) || seenNames[presented] || connectorControlTool(name) {
				return nil, connectorUpstreamProtocolError{}
			}
			seenNames[presented] = true
		}
		encoded, err := json.Marshal(list)
		if err != nil {
			return nil, err
		}
		bytes += len(encoded)
		if bytes > connectorRecoveryMaxBytes {
			return nil, errConnectorRecoveryLimit
		}
		tools = append(tools, list["tools"].([]map[string]any)...)
		next, _ := list["nextCursor"].(string)
		if next == "" {
			return tools, nil
		}
		if seenCursors[next] {
			return nil, connectorUpstreamProtocolError{}
		}
		seenCursors[next], cursor = true, next
	}
	return nil, errConnectorRecoveryLimit
}
