package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/featureflags"
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
)

const (
	agentMCPDelegateTool = "delegate_task"
	agentMCPGetTaskTool  = "get_task"
)

type agentMCPDelegateArguments struct {
	Instruction string `json:"instruction"`
	RequestID   string `json:"request_id,omitempty"`
}

type agentMCPGetTaskArguments struct {
	TaskID string `json:"task_id"`
}

// HandleAgentMCP exposes one hosted Agent as a two-tool Streamable HTTP MCP
// server. MCP credentials use the same revocable credential substrate as A2A,
// but the MCP link remains usable independently of A2A publication state.
// The /connect/{accessToken} route is an intentionally simple capability URL
// for clients such as Codex that cannot persist a literal HTTP auth header.
func (h *Handler) HandleAgentMCP(w http.ResponseWriter, r *http.Request) {
	if !featureflags.AgentA2AInboundEnabled(r.Context(), h.FeatureFlags) {
		http.NotFound(w, r)
		return
	}
	if h.A2AService == nil {
		writeError(w, http.StatusServiceUnavailable, "Agent execution service is unavailable")
		return
	}
	if !h.multicaMCPOriginAllowed(r) {
		writeError(w, http.StatusForbidden, "untrusted MCP Origin")
		return
	}

	rawToken, ok := parseAgentAccessToken(r, chi.URLParam(r, "accessToken"))
	if !ok {
		writeAgentMCPUnauthorized(w)
		return
	}
	credential, err := h.Queries.GetAgentA2ACredentialByTokenHash(r.Context(), auth.HashToken(rawToken))
	if err != nil {
		writeAgentMCPUnauthorized(w)
		return
	}
	requestedAgentID := strings.TrimSpace(chi.URLParam(r, "publicAgentId"))
	if (requestedAgentID != "" && requestedAgentID != credential.PublicAgentID) ||
		!credential.AgentOwnerID.Valid || !credential.DelegatedByUserID.Valid ||
		credential.AgentOwnerID.Bytes != credential.DelegatedByUserID.Bytes {
		writeAgentMCPUnauthorized(w)
		return
	}

	principal := a2aintegration.Principal{
		WorkspaceID:           uuidToString(credential.WorkspaceID),
		AgentID:               uuidToString(credential.AgentID),
		EndpointID:            uuidToString(credential.EndpointID),
		PublicAgentID:         credential.PublicAgentID,
		ClientID:              uuidToString(credential.ClientID),
		CredentialID:          uuidToString(credential.CredentialID),
		Scopes:                credential.ClientScopes,
		EndpointEnabled:       credential.EndpointEnabled,
		AllowDisabledEndpoint: true,
	}
	ctx := a2aintegration.WithPrincipal(r.Context(), principal)
	_ = h.Queries.TouchAgentA2ACredentialLastUsed(ctx, credential.CredentialID)

	// A capability URL is secret-bearing. Keep it out of downstream application
	// contexts and prevent browser caches/referrers from propagating it.
	r.Header.Del("Authorization")
	r.Header.Del("X-API-Key")
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Referrer-Policy", "no-referrer")

	r.Body = http.MaxBytesReader(w, r.Body, multicaMCPMaxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	var req multicaMCPRequest
	if err := decoder.Decode(&req); err != nil {
		h.writeMulticaMCPError(w, nil, -32700, "parse error")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		h.writeMulticaMCPError(w, req.ID, -32600, "invalid request")
		return
	}
	if req.JSONRPC != "2.0" || strings.TrimSpace(req.Method) == "" {
		h.writeMulticaMCPError(w, req.ID, -32600, "invalid request")
		return
	}
	if !multicaMCPRequestHasID(req.ID) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if req.Method != "initialize" && !supportedMulticaMCPProtocolVersion(r.Header.Get("MCP-Protocol-Version")) {
		writeError(w, http.StatusBadRequest, "unsupported MCP protocol version")
		return
	}

	r = r.WithContext(ctx)
	switch req.Method {
	case "initialize":
		h.handleAgentMCPInitialize(w, req)
	case "ping":
		h.writeMulticaMCPResult(w, req.ID, map[string]any{})
	case "tools/list":
		h.writeMulticaMCPResult(w, req.ID, map[string]any{"tools": agentMCPToolDefinitions()})
	case "tools/call":
		h.handleAgentMCPToolsCall(w, r, req, principal)
	default:
		h.writeMulticaMCPError(w, req.ID, -32601, "method not found")
	}
}

func (h *Handler) handleAgentMCPInitialize(w http.ResponseWriter, req multicaMCPRequest) {
	protocolVersion := multicaMCPProtocolVersion
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(req.Params) > 0 && json.Unmarshal(req.Params, &params) == nil && params.ProtocolVersion == multicaMCPCompatProtocolVersion {
		protocolVersion = multicaMCPCompatProtocolVersion
	}
	h.writeMulticaMCPResult(w, req.ID, map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]string{"name": "multica-hosted-agent", "version": "1.0.0"},
	})
}

func agentMCPToolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        agentMCPDelegateTool,
			"title":       "Delegate work to this Multica Agent",
			"description": "Start an asynchronous task on the hosted Multica Agent. Use get_task with the returned task id until it reaches a terminal state.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"instruction": map[string]any{"type": "string", "minLength": 1, "description": "The complete task instruction."},
					"request_id":  map[string]any{"type": "string", "minLength": 1, "maxLength": 256, "description": "Optional stable idempotency key for safe retries."},
				},
				"required": []string{"instruction"},
			},
			"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": true},
		},
		{
			"name":        agentMCPGetTaskTool,
			"title":       "Get a delegated Multica task",
			"description": "Read the current state and final text artifact of a task previously created through this connection.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"task_id": map[string]any{"type": "string", "minLength": 1}},
				"required":   []string{"task_id"},
			},
			"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		},
	}
}

func (h *Handler) handleAgentMCPToolsCall(w http.ResponseWriter, r *http.Request, req multicaMCPRequest, principal a2aintegration.Principal) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.Name) == "" {
		h.writeMulticaMCPError(w, req.ID, -32602, "invalid tool call parameters")
		return
	}
	if len(params.Arguments) == 0 {
		params.Arguments = json.RawMessage(`{}`)
	}

	var result any
	var err error
	switch params.Name {
	case agentMCPDelegateTool:
		if !agentMCPHasScope(principal.Scopes, "send") {
			h.writeMulticaMCPToolError(w, req.ID, "this connection cannot delegate tasks")
			return
		}
		var args agentMCPDelegateArguments
		if decodeAgentMCPArguments(params.Arguments, &args) != nil || strings.TrimSpace(args.Instruction) == "" {
			h.writeMulticaMCPError(w, req.ID, -32602, "invalid delegate_task arguments")
			return
		}
		requestID := strings.TrimSpace(args.RequestID)
		if requestID == "" {
			requestID = a2a.NewMessageID()
		}
		result, err = h.A2AService.SendMessage(r.Context(), &a2a.SendMessageRequest{
			Config:  &a2a.SendMessageConfig{ReturnImmediately: true, AcceptedOutputModes: []string{"text/plain"}},
			Message: &a2a.Message{ID: requestID, Role: a2a.MessageRoleUser, Parts: a2a.ContentParts{a2a.NewTextPart(args.Instruction)}},
		})
	case agentMCPGetTaskTool:
		if !agentMCPHasScope(principal.Scopes, "read") {
			h.writeMulticaMCPToolError(w, req.ID, "this connection cannot read tasks")
			return
		}
		var args agentMCPGetTaskArguments
		if decodeAgentMCPArguments(params.Arguments, &args) != nil || strings.TrimSpace(args.TaskID) == "" {
			h.writeMulticaMCPError(w, req.ID, -32602, "invalid get_task arguments")
			return
		}
		result, err = h.A2AService.GetTask(r.Context(), &a2a.GetTaskRequest{ID: a2a.TaskID(strings.TrimSpace(args.TaskID))})
	default:
		h.writeMulticaMCPError(w, req.ID, -32602, "unknown tool")
		return
	}
	if err != nil {
		slog.Info("hosted Agent MCP call rejected", "tool", params.Name, "public_agent_id", principal.PublicAgentID, "error", err)
		h.writeMulticaMCPToolError(w, req.ID, err.Error())
		return
	}
	payload, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		h.writeMulticaMCPToolError(w, req.ID, "failed to encode Agent task")
		return
	}
	h.writeMulticaMCPResult(w, req.ID, multicaMCPToolResult{
		Content:           []multicaMCPContent{{Type: "text", Text: string(payload)}},
		StructuredContent: result,
	})
}

func decodeAgentMCPArguments(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func agentMCPHasScope(scopes []string, target string) bool {
	for _, scope := range scopes {
		if scope == target {
			return true
		}
	}
	return false
}

func writeAgentMCPUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="multica-agent"`)
	writeError(w, http.StatusUnauthorized, "invalid Agent access credential")
}
