package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/integrations/agentmessagerouter"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	multicaMCPProtocolVersion       = "2025-06-18"
	multicaMCPCompatProtocolVersion = "2025-03-26"
	multicaMCPChatSendTool          = "chat_send_message"
	multicaMCPBindingGetTool        = "get_digital_employee_binding"
	multicaMCPBindingBindTool       = "bind_digital_employee_to_multica_agent"
	multicaMCPBindingUnbindTool     = "unbind_digital_employee"
	multicaMCPMaxRequestBytes       = 1 << 20
	multicaMCPPersonalTokenPrefix   = "mul_"
	multicaMCPForwardedFromTaskContextKey    = "mcp_forwarded_from_task_id"
	multicaMCPForwardedFromSessionContextKey = "mcp_forwarded_from_chat_session_id"
	multicaMCPForwardedFromAgentContextKey   = "mcp_forwarded_from_agent_id"
)

const multicaMCPChatSendToolDescription = `Authorized server-provided Multica action: send a message to an existing Chat session owned by the authenticated user and queue that Chat's Agent to continue.

With a personal access token, the message is sent as the authenticated member. With a task token, it is sent as the current Agent and records task-to-task forwarding provenance.

This managed MCP tool is not a curl/raw-API workaround. Use it when information should continue work in an existing Chat. A task token must target a different Chat from its source task.`

const multicaMCPBindingGetToolDescription = `Get Multica's local digital employee binding and reconcile it with Router.

With a personal access token, supply agent_id for an Agent the authenticated member may manage and invoke. With a task token, the target is fixed to the authenticated task Agent.`

const multicaMCPBindingBindToolDescription = `Bind a DingTalk digital employee created through DWS to a Multica Agent.

With a personal access token, supply agent_id for an Agent the authenticated member may manage and invoke. With a task token, the target is fixed to the authenticated task Agent.

Multica issues and consumes the one-time Router credential server-side and never exposes it to the caller.

Existing ownership is never taken over.`

const multicaMCPBindingUnbindToolDescription = `Conditionally unbind a Multica Agent's current digital employee.

With a personal access token, supply agent_id for an Agent the authenticated member may manage and invoke. With a task token, the target is fixed to the authenticated task Agent.

Router ownership is checked authoritatively before Multica clears its local projection.`

type multicaMCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type multicaMCPResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *multicaMCPError `json:"error,omitempty"`
}

type multicaMCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type multicaMCPToolResult struct {
	Content           []multicaMCPContent `json:"content"`
	StructuredContent any                 `json:"structuredContent,omitempty"`
	IsError           bool                `json:"isError,omitempty"`
}

type multicaMCPContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type multicaMCPChatSendArguments struct {
	SessionID string `json:"session_id"`
	Content   string `json:"content"`
}

type multicaMCPChatSendResult struct {
	SessionID string `json:"session_id"`
	MessageID string `json:"message_id"`
	TaskID    string `json:"task_id"`
	TraceID   string `json:"trace_id"`
	CreatedAt string `json:"created_at"`
}

type multicaMCPDigitalEmployeeBindArguments struct {
	AgentID           string                                             `json:"agent_id"`
	TenantID          string                                             `json:"tenant_id"`
	DigitalEmployeeID string                                             `json:"digital_employee_id"`
	SurfaceType       string                                             `json:"surface_type"`
	MessageScope      string                                             `json:"message_scope"`
	EnabledDomains    []string                                           `json:"enabled_domains"`
	Conversations     []agentmessagerouter.DingTalkConversationSnapshot `json:"conversations"`
}

type multicaMCPAgentArguments struct {
	AgentID string `json:"agent_id"`
}

type multicaMCPBindingTaskStore interface {
	GetAgentTaskInWorkspace(context.Context, db.GetAgentTaskInWorkspaceParams) (db.AgentTaskQueue, error)
}

type DigitalEmployeeBindingMCPService interface {
	BindDigitalEmployee(context.Context, agentmessagerouter.DirectBindingParams) (agentmessagerouter.DirectDigitalEmployeeBinding, error)
	GetDigitalEmployeeBinding(context.Context, pgtype.UUID, pgtype.UUID) (agentmessagerouter.DirectDigitalEmployeeBinding, error)
	Unbind(context.Context, agentmessagerouter.UnbindParams) (agentmessagerouter.PublicDingTalkAccountBinding, error)
}

type multicaMCPBindingIdentity struct {
	WorkspaceID pgtype.UUID
	AgentID     pgtype.UUID
	Originator  pgtype.UUID
	Agent       db.Agent
	Workspace   db.Workspace
}

type multicaMCPToolCallError struct {
	message string
}

func (e *multicaMCPToolCallError) Error() string { return e.message }

func (h *Handler) multicaMCPChatSendEnabled(r *http.Request) bool {
	return h != nil && featureflags.MulticaMCPChatSendEnabled(r.Context(), h.FeatureFlags)
}

// MulticaMCP is a stateless MCP Streamable HTTP endpoint. V1 does not expose
// server-initiated messages, so GET correctly reports 405 and every JSON-RPC
// exchange is completed by one POST response.
func (h *Handler) MulticaMCP(w http.ResponseWriter, r *http.Request) {
	if !h.multicaMCPChatSendEnabled(r) {
		writeError(w, http.StatusServiceUnavailable, "Multica MCP is not enabled")
		return
	}
	if !multicaMCPTaskTokenAuthenticated(r) && !multicaMCPPersonalTokenAuthenticated(r) {
		writeError(w, http.StatusForbidden, "Multica MCP requires a task token or personal access token")
		return
	}
	if !h.multicaMCPOriginAllowed(r) {
		writeError(w, http.StatusForbidden, "untrusted MCP Origin")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

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
		// JSON-RPC notifications never receive a response. tools/call without an
		// id is also ignored rather than performing a write the caller cannot
		// correlate or safely retry.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if req.Method != "initialize" && !supportedMulticaMCPProtocolVersion(r.Header.Get("MCP-Protocol-Version")) {
		writeError(w, http.StatusBadRequest, "unsupported MCP protocol version")
		return
	}

	switch req.Method {
	case "initialize":
		h.handleMulticaMCPInitialize(w, req)
	case "ping":
		h.writeMulticaMCPResult(w, req.ID, map[string]any{})
	case "tools/list":
		h.writeMulticaMCPResult(w, req.ID, map[string]any{"tools": multicaMCPToolDefinitions()})
	case "tools/call":
		h.handleMulticaMCPToolsCall(w, r, req)
	default:
		h.writeMulticaMCPError(w, req.ID, -32601, "method not found")
	}
}

func multicaMCPTaskTokenAuthenticated(r *http.Request) bool {
	return r.Header.Get("X-Actor-Source") == "task_token"
}

func multicaMCPPersonalTokenAuthenticated(r *http.Request) bool {
	// The route is behind Auth and workspace membership middleware. Auth has
	// already validated the mul_ credential, stripped any caller-supplied
	// X-Actor-Source, and stamped X-User-ID before this discriminator runs.
	if r.Header.Get("X-Actor-Source") != "" || strings.TrimSpace(r.Header.Get("X-User-ID")) == "" {
		return false
	}
	scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	return ok && strings.EqualFold(scheme, "Bearer") && strings.HasPrefix(strings.TrimSpace(token), multicaMCPPersonalTokenPrefix)
}

func multicaMCPToolDefinitions() []any {
	return []any{
		multicaMCPChatSendDefinition(),
		multicaMCPBindingGetDefinition(),
		multicaMCPBindingBindDefinition(),
		multicaMCPBindingUnbindDefinition(),
	}
}

func multicaMCPBindingGetDefinition() map[string]any {
	return map[string]any{
		"name":        multicaMCPBindingGetTool,
		"title":       "Get this Agent's digital employee binding",
		"description": multicaMCPBindingGetToolDescription,
		"inputSchema": multicaMCPAgentSelectionSchema(),
		"annotations": map[string]any{
			"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false,
		},
	}
}

func multicaMCPBindingBindDefinition() map[string]any {
	return map[string]any{
		"name":        multicaMCPBindingBindTool,
		"title":       "Bind a DingTalk digital employee to this Agent",
		"description": multicaMCPBindingBindToolDescription,
		"inputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"agent_id":             multicaMCPAgentIDProperty(),
				"tenant_id":            map[string]any{"type": "string", "minLength": 1, "description": "DingTalk organization identifier returned by DWS."},
				"digital_employee_id":  map[string]any{"type": "string", "minLength": 1, "description": "Digital employee account identifier returned by DWS."},
				"surface_type":         map[string]any{"type": "string", "enum": []string{"issue", "chat", "auto"}, "default": "auto"},
				"message_scope":        map[string]any{"type": "string", "enum": []string{"direct_only", "custom", "all"}, "default": "direct_only"},
				"enabled_domains":      map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1}, "default": []string{"channel"}},
				"conversations": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"required":             []string{"cid", "name"},
						"properties": map[string]any{
							"cid":             map[string]any{"type": "string", "minLength": 1},
							"name":            map[string]any{"type": "string", "minLength": 1},
							"avatar_media_id": map[string]any{"type": "string"},
							"avatar_url":      map[string]any{"type": "string"},
						},
					},
				},
			},
			"required": []string{"tenant_id", "digital_employee_id"},
		},
		"annotations": map[string]any{
			"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false,
		},
	}
}

func multicaMCPBindingUnbindDefinition() map[string]any {
	return map[string]any{
		"name":        multicaMCPBindingUnbindTool,
		"title":       "Unbind this Agent's digital employee",
		"description": multicaMCPBindingUnbindToolDescription,
		"inputSchema": multicaMCPAgentSelectionSchema(),
		"annotations": map[string]any{
			"readOnlyHint": false, "destructiveHint": true, "idempotentHint": true, "openWorldHint": false,
		},
	}
}

func multicaMCPAgentSelectionSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"agent_id": multicaMCPAgentIDProperty(),
		},
	}
}

func multicaMCPAgentIDProperty() map[string]any {
	return map[string]any{
		"type": "string",
		"description": "Agent UUID. Required with a personal access token; omit with a task token to use the authenticated task Agent.",
	}
}

func multicaMCPRequestHasID(id json.RawMessage) bool {
	trimmed := bytes.TrimSpace(id)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func supportedMulticaMCPProtocolVersion(raw string) bool {
	raw = strings.TrimSpace(raw)
	// The Streamable HTTP compatibility rule treats an absent header as the
	// 2025-03-26 revision. Accept both that revision and the current one.
	return raw == "" || raw == multicaMCPProtocolVersion || raw == multicaMCPCompatProtocolVersion
}

func (h *Handler) multicaMCPOriginAllowed(r *http.Request) bool {
	origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")
	if origin == "" {
		return true
	}
	cfg := h.currentConfig()
	for _, allowed := range []string{cfg.PublicURL, cfg.AppURL, cfg.FrontendOrigin} {
		if strings.EqualFold(origin, strings.TrimRight(strings.TrimSpace(allowed), "/")) && strings.TrimSpace(allowed) != "" {
			return true
		}
	}
	return false
}

func (h *Handler) handleMulticaMCPInitialize(w http.ResponseWriter, req multicaMCPRequest) {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			h.writeMulticaMCPError(w, req.ID, -32602, "invalid initialize parameters")
			return
		}
	}
	protocolVersion := multicaMCPProtocolVersion
	if params.ProtocolVersion == multicaMCPCompatProtocolVersion || params.ProtocolVersion == multicaMCPProtocolVersion {
		protocolVersion = params.ProtocolVersion
	}
	h.writeMulticaMCPResult(w, req.ID, map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]string{
			"name":    "multica",
			"version": "1.0.0",
		},
	})
}

func multicaMCPChatSendDefinition() map[string]any {
	return map[string]any{
		"name":        multicaMCPChatSendTool,
		"title":       "Continue an existing Multica Chat",
		"description": multicaMCPChatSendToolDescription,
		"inputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"session_id": map[string]any{"type": "string", "description": "Target Multica Chat session UUID."},
				"content":    map[string]any{"type": "string", "minLength": 1, "description": "Message to deliver verbatim to the target Chat."},
			},
			"required": []string{"session_id", "content"},
		},
		"outputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{"type": "string"},
				"message_id": map[string]any{"type": "string"},
				"task_id":    map[string]any{"type": "string"},
				"trace_id":   map[string]any{"type": "string"},
				"created_at": map[string]any{"type": "string"},
			},
			"required": []string{"session_id", "message_id", "task_id", "trace_id", "created_at"},
		},
		"annotations": map[string]any{
			"readOnlyHint":    false,
			"destructiveHint": false,
			"idempotentHint":  false,
			"openWorldHint":   false,
		},
	}
}

func (h *Handler) handleMulticaMCPToolsCall(w http.ResponseWriter, r *http.Request, req multicaMCPRequest) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || strings.TrimSpace(params.Name) == "" {
		h.writeMulticaMCPError(w, req.ID, -32602, "invalid tool call parameters")
		return
	}
	if params.Name == multicaMCPBindingGetTool || params.Name == multicaMCPBindingBindTool || params.Name == multicaMCPBindingUnbindTool {
		h.handleMulticaMCPDigitalEmployeeCall(w, r, req.ID, params.Name, params.Arguments)
		return
	}
	if params.Name != multicaMCPChatSendTool {
		h.writeMulticaMCPError(w, req.ID, -32602, "unknown tool")
		return
	}
	var args multicaMCPChatSendArguments
	decoder := json.NewDecoder(bytes.NewReader(params.Arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		h.writeMulticaMCPError(w, req.ID, -32602, "invalid chat_send_message arguments")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		h.writeMulticaMCPError(w, req.ID, -32602, "invalid chat_send_message arguments")
		return
	}
	args.SessionID = strings.TrimSpace(args.SessionID)
	if args.SessionID == "" || strings.TrimSpace(args.Content) == "" {
		h.writeMulticaMCPError(w, req.ID, -32602, "session_id and content are required")
		return
	}

	result, err := h.callMulticaMCPChatSend(r, args)
	if err != nil {
		var toolErr *multicaMCPToolCallError
		if !errors.As(err, &toolErr) {
			slog.Error("Multica MCP chat send failed", "source_task_id", r.Header.Get("X-Task-ID"), "error", err)
			toolErr = &multicaMCPToolCallError{message: "failed to continue target Chat"}
		}
		h.writeMulticaMCPResult(w, req.ID, multicaMCPToolResult{
			Content: []multicaMCPContent{{Type: "text", Text: toolErr.message}},
			IsError: true,
		})
		return
	}
	textResult, _ := json.Marshal(result)
	h.writeMulticaMCPResult(w, req.ID, multicaMCPToolResult{
		Content:           []multicaMCPContent{{Type: "text", Text: string(textResult)}},
		StructuredContent: result,
	})
}

func (h *Handler) handleMulticaMCPDigitalEmployeeCall(
	w http.ResponseWriter,
	r *http.Request,
	id json.RawMessage,
	toolName string,
	rawArguments json.RawMessage,
) {
	service := h.DigitalEmployeeBindingMCPBindings
	if service == nil {
		h.writeMulticaMCPToolError(w, id, "Digital employee binding is not configured")
		return
	}
	var agentArgs multicaMCPAgentArguments
	var bindArgs multicaMCPDigitalEmployeeBindArguments
	switch toolName {
	case multicaMCPBindingGetTool:
		if err := decodeMulticaMCPArguments(rawArguments, &agentArgs); err != nil {
			h.writeMulticaMCPToolError(w, id, "invalid get_digital_employee_binding arguments")
			return
		}
	case multicaMCPBindingBindTool:
		if err := decodeMulticaMCPArguments(rawArguments, &bindArgs); err != nil {
			h.writeMulticaMCPToolError(w, id, "invalid bind_digital_employee_to_multica_agent arguments")
			return
		}
		agentArgs.AgentID = bindArgs.AgentID
	case multicaMCPBindingUnbindTool:
		if err := decodeMulticaMCPArguments(rawArguments, &agentArgs); err != nil {
			h.writeMulticaMCPToolError(w, id, "invalid unbind_digital_employee arguments")
			return
		}
	}
	identity, err := h.resolveMulticaMCPBindingIdentity(r, agentArgs.AgentID)
	if err != nil {
		h.writeMulticaMCPToolError(w, id, multicaMCPDigitalEmployeeErrorMessage(err))
		return
	}

	var result any
	switch toolName {
	case multicaMCPBindingGetTool:
		result, err = service.GetDigitalEmployeeBinding(r.Context(), identity.WorkspaceID, identity.AgentID)
	case multicaMCPBindingBindTool:
		bindArgs.TenantID = strings.TrimSpace(bindArgs.TenantID)
		bindArgs.DigitalEmployeeID = strings.TrimSpace(bindArgs.DigitalEmployeeID)
		if bindArgs.TenantID == "" || bindArgs.DigitalEmployeeID == "" {
			h.writeMulticaMCPToolError(w, id, "tenant_id and digital_employee_id are required")
			return
		}
		if strings.TrimSpace(bindArgs.SurfaceType) == "" {
			bindArgs.SurfaceType = agentmessagerouter.DingTalkSurfaceAuto
		}
		if strings.TrimSpace(bindArgs.MessageScope) == "" {
			bindArgs.MessageScope = agentmessagerouter.DingTalkMessageScopeDirectOnly
		}
		if len(bindArgs.EnabledDomains) == 0 {
			bindArgs.EnabledDomains = []string{"channel"}
		}
		result, err = service.BindDigitalEmployee(r.Context(), agentmessagerouter.DirectBindingParams{
			Agent: agentmessagerouter.BeginAgent{
				ID:   identity.AgentID,
				Name: identity.Agent.Name,
				Workspace: agentmessagerouter.BeginWorkspace{
					ID:   identity.WorkspaceID,
					Name: identity.Workspace.Name,
				},
			},
			InitiatorID:       identity.Originator,
			TenantID:          bindArgs.TenantID,
			DigitalEmployeeID: bindArgs.DigitalEmployeeID,
			SurfaceType:       bindArgs.SurfaceType,
			MessageScope:      bindArgs.MessageScope,
			EnabledDomains:    bindArgs.EnabledDomains,
			Conversations:     bindArgs.Conversations,
		})
	case multicaMCPBindingUnbindTool:
		result, err = service.Unbind(r.Context(), agentmessagerouter.UnbindParams{
			WorkspaceID: identity.WorkspaceID,
			AgentID:     identity.AgentID,
			BindingMode: agentmessagerouter.BindingModeMessage,
		})
	}
	if err != nil {
		message := multicaMCPDigitalEmployeeErrorMessage(err)
		slog.Warn("Multica MCP digital employee action failed",
			"tool", toolName,
			"source_task_id", r.Header.Get("X-Task-ID"),
			"workspace_id", util.UUIDToString(identity.WorkspaceID),
			"agent_id", util.UUIDToString(identity.AgentID),
			"error_class", message,
		)
		h.writeMulticaMCPToolError(w, id, message)
		return
	}
	slog.Info("Multica MCP digital employee action completed",
		"tool", toolName,
		"source_task_id", r.Header.Get("X-Task-ID"),
		"workspace_id", util.UUIDToString(identity.WorkspaceID),
		"agent_id", util.UUIDToString(identity.AgentID),
	)
	payload, _ := json.Marshal(result)
	h.writeMulticaMCPResult(w, id, multicaMCPToolResult{
		Content:           []multicaMCPContent{{Type: "text", Text: string(payload)}},
		StructuredContent: result,
	})
}

func (h *Handler) resolveMulticaMCPBindingIdentity(r *http.Request, requestedAgentID string) (multicaMCPBindingIdentity, error) {
	workspaceID := ctxWorkspaceID(r.Context())
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(r.Header.Get("X-Workspace-ID"))
	}
	workspaceUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: "authenticated workspace is invalid"}
	}
	requestedAgentID = strings.TrimSpace(requestedAgentID)
	var agentUUID pgtype.UUID
	var originator pgtype.UUID
	if multicaMCPTaskTokenAuthenticated(r) {
		taskUUID, err := util.ParseUUID(strings.TrimSpace(r.Header.Get("X-Task-ID")))
		if err != nil {
			return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: "authenticated source task is invalid"}
		}
		taskStore := h.multicaMCPBindingTasks
		if taskStore == nil {
			taskStore = h.Queries
		}
		if taskStore == nil {
			return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: "Digital employee binding is not configured"}
		}
		task, err := taskStore.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{
			ID: taskUUID, WorkspaceID: workspaceUUID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: "authenticated source task was not found"}
		}
		if err != nil {
			return multicaMCPBindingIdentity{}, err
		}
		authenticatedAgentID := strings.TrimSpace(r.Header.Get("X-Agent-ID"))
		if authenticatedAgentID == "" || authenticatedAgentID != util.UUIDToString(task.AgentID) {
			return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: "authenticated Agent does not own the source task"}
		}
		if requestedAgentID != "" {
			requestedAgentUUID, parseErr := util.ParseUUID(requestedAgentID)
			if parseErr != nil {
				return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: "agent_id must be a valid UUID"}
			}
			if requestedAgentUUID != task.AgentID {
				return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: "task token cannot select a different Agent"}
			}
		}
		if task.Status != "running" && task.Status != "dispatched" {
			return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: "source task is not active"}
		}
		agentUUID = task.AgentID
		originator = sourceTaskOriginator(task)
		if !originator.Valid {
			return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: "source task has no human originator"}
		}
	} else {
		if requestedAgentID == "" {
			return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: "agent_id is required with a personal access token"}
		}
		agentUUID, err = util.ParseUUID(requestedAgentID)
		if err != nil {
			return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: "agent_id must be a valid UUID"}
		}
		originator, err = util.ParseUUID(strings.TrimSpace(r.Header.Get("X-User-ID")))
		if err != nil {
			return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: "authenticated user is invalid"}
		}
	}
	metadataStore := h.dingTalkAccountBindingMetadata
	if metadataStore == nil {
		metadataStore = h.Queries
	}
	if metadataStore == nil {
		return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: "Digital employee binding is not configured"}
	}
	agent, err := metadataStore.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID: agentUUID, WorkspaceID: workspaceUUID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: "authenticated Agent was not found"}
	}
	if err != nil {
		return multicaMCPBindingIdentity{}, err
	}
	if agent.ArchivedAt.Valid {
		return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: "authenticated Agent is archived"}
	}
	permissionStore := h.dingTalkAccountBindingPermissions
	if permissionStore == nil {
		permissionStore = h.Queries
	}
	if permissionStore == nil || !canOperateDingTalkAccountBinding(
		r.Context(), permissionStore, agent, originator, workspaceUUID,
	) {
		return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: dingTalkAccountBindingForbidden}
	}
	workspace, err := metadataStore.GetWorkspace(r.Context(), workspaceUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: "authenticated workspace was not found"}
	}
	if err != nil {
		return multicaMCPBindingIdentity{}, err
	}
	return multicaMCPBindingIdentity{
		WorkspaceID: workspaceUUID,
		AgentID:     agentUUID,
		Originator:  originator,
		Agent:       agent,
		Workspace:   workspace,
	}, nil
}

func decodeMulticaMCPArguments(raw json.RawMessage, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("invalid tool arguments")
	}
	return nil
}

func multicaMCPDigitalEmployeeErrorMessage(err error) string {
	var toolErr *multicaMCPToolCallError
	if errors.As(err, &toolErr) {
		return toolErr.message
	}
	switch {
	case errors.Is(err, agentmessagerouter.ErrNotConfigured):
		return "Digital employee binding is not configured"
	case errors.Is(err, agentmessagerouter.ErrNotFound):
		return "Digital employee binding was not found"
	case errors.Is(err, agentmessagerouter.ErrAlreadyActive), errors.Is(err, agentmessagerouter.ErrBindingConflict):
		return "Digital employee binding conflicts with the current ownership state"
	case errors.Is(err, agentmessagerouter.ErrInvalidResult):
		return "Digital employee binding arguments or state are invalid"
	case errors.Is(err, agentmessagerouter.ErrRouterUnavailable):
		return "Router is temporarily unavailable; retry later"
	default:
		return "Digital employee binding failed"
	}
}

func (h *Handler) writeMulticaMCPToolError(w http.ResponseWriter, id json.RawMessage, message string) {
	h.writeMulticaMCPResult(w, id, multicaMCPToolResult{
		Content: []multicaMCPContent{{Type: "text", Text: message}},
		IsError: true,
	})
}

func (h *Handler) callMulticaMCPChatSend(r *http.Request, args multicaMCPChatSendArguments) (multicaMCPChatSendResult, error) {
	if multicaMCPPersonalTokenAuthenticated(r) {
		return h.callMulticaMCPPersonalChatSend(r, args)
	}
	return h.callMulticaMCPTaskChatSend(r, args)
}

func (h *Handler) callMulticaMCPPersonalChatSend(r *http.Request, args multicaMCPChatSendArguments) (multicaMCPChatSendResult, error) {
	workspaceID := ctxWorkspaceID(r.Context())
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(r.Header.Get("X-Workspace-ID"))
	}
	workspaceUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "authenticated workspace is invalid"}
	}
	userID := strings.TrimSpace(r.Header.Get("X-User-ID"))
	userUUID, err := util.ParseUUID(userID)
	if err != nil {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "authenticated user is invalid"}
	}
	targetSessionUUID, err := util.ParseUUID(args.SessionID)
	if err != nil {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "session_id must be a valid UUID"}
	}
	if h.Queries == nil || h.TaskService == nil {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "target Chat service is unavailable"}
	}
	targetSession, err := h.Queries.GetChatSessionInWorkspace(r.Context(), db.GetChatSessionInWorkspaceParams{
		ID:          targetSessionUUID,
		WorkspaceID: workspaceUUID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "target Chat was not found"}
	}
	if err != nil {
		return multicaMCPChatSendResult{}, err
	}
	if targetSession.CreatorID != userUUID {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "target Chat is not owned by the authenticated user"}
	}
	if targetSession.Status != "active" {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "target Chat is archived"}
	}
	targetAgent, err := h.Queries.GetAgent(r.Context(), targetSession.AgentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "target Chat Agent was not found"}
	}
	if err != nil {
		return multicaMCPChatSendResult{}, err
	}
	resolvedWorkspaceID := uuidToString(workspaceUUID)
	if targetAgent.WorkspaceID != workspaceUUID {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "target Chat Agent is outside the authenticated workspace"}
	}
	if targetAgent.ArchivedAt.Valid {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "target Chat Agent is archived"}
	}
	if !targetAgent.RuntimeID.Valid {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "target Chat Agent has no runtime"}
	}
	if !h.canInvokeAgent(r.Context(), targetAgent, "member", userID, userID, resolvedWorkspaceID) {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "authenticated user cannot invoke the target Chat Agent"}
	}

	trace := chattrace.New("mcp")
	sent, err := h.TaskService.SendDirectChatMessage(
		r.Context(),
		targetSession,
		targetAgent,
		userUUID,
		args.Content,
		nil,
		"member",
		userUUID,
		trace,
	)
	if err != nil {
		return multicaMCPChatSendResult{}, err
	}
	resolvedSessionID := uuidToString(targetSession.ID)
	h.publishChat(protocol.EventChatMessage, resolvedWorkspaceID, "member", userID, resolvedSessionID, protocol.ChatMessagePayload{
		ChatSessionID: resolvedSessionID,
		MessageID:     uuidToString(sent.Message.ID),
		Role:          "user",
		Content:       args.Content,
		TaskID:        uuidToString(sent.Task.ID),
		CreatedAt:     timestampToString(sent.Message.CreatedAt),
		TraceID:       trace.TraceID,
	})
	slog.Info("Multica MCP personal Chat continuation queued",
		"user_id", userID,
		"target_chat_session_id", resolvedSessionID,
		"target_task_id", uuidToString(sent.Task.ID),
	)
	return multicaMCPChatSendResult{
		SessionID: resolvedSessionID,
		MessageID: uuidToString(sent.Message.ID),
		TaskID:    uuidToString(sent.Task.ID),
		TraceID:   trace.TraceID,
		CreatedAt: timestampToString(sent.Task.CreatedAt),
	}, nil
}

func (h *Handler) callMulticaMCPTaskChatSend(r *http.Request, args multicaMCPChatSendArguments) (multicaMCPChatSendResult, error) {
	workspaceID := ctxWorkspaceID(r.Context())
	if workspaceID == "" {
		workspaceID = strings.TrimSpace(r.Header.Get("X-Workspace-ID"))
	}
	workspaceUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "authenticated workspace is invalid"}
	}
	sourceTaskID := strings.TrimSpace(r.Header.Get("X-Task-ID"))
	sourceTaskUUID, err := util.ParseUUID(sourceTaskID)
	if err != nil {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "authenticated source task is invalid"}
	}
	sourceTask, err := h.Queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{
		ID:          sourceTaskUUID,
		WorkspaceID: workspaceUUID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "authenticated source task was not found"}
	}
	if err != nil {
		return multicaMCPChatSendResult{}, err
	}
	sourceAgentID := strings.TrimSpace(r.Header.Get("X-Agent-ID"))
	if sourceAgentID == "" || sourceAgentID != uuidToString(sourceTask.AgentID) {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "authenticated Agent does not own the source task"}
	}
	if !sourceTask.ChatSessionID.Valid {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "only a Chat task can continue another Chat"}
	}
	if sourceTask.Status != "running" && sourceTask.Status != "dispatched" {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "source task is not active"}
	}

	targetSessionUUID, err := util.ParseUUID(args.SessionID)
	if err != nil {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "session_id must be a valid UUID"}
	}
	if targetSessionUUID == sourceTask.ChatSessionID {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "target Chat must be different from the source Chat"}
	}
	targetSession, err := h.Queries.GetChatSessionInWorkspace(r.Context(), db.GetChatSessionInWorkspaceParams{
		ID:          targetSessionUUID,
		WorkspaceID: workspaceUUID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "target Chat was not found"}
	}
	if err != nil {
		return multicaMCPChatSendResult{}, err
	}
	taskOwnerID := strings.TrimSpace(r.Header.Get("X-User-ID"))
	if taskOwnerID == "" || uuidToString(targetSession.CreatorID) != taskOwnerID {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "target Chat is not owned by the authenticated task owner"}
	}
	if targetSession.Status != "active" {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "target Chat is archived"}
	}
	targetAgent, err := h.Queries.GetAgent(r.Context(), targetSession.AgentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "target Chat Agent was not found"}
	}
	if err != nil {
		return multicaMCPChatSendResult{}, err
	}
	if uuidToString(targetAgent.WorkspaceID) != workspaceID {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "target Chat Agent is outside the authenticated workspace"}
	}
	if targetAgent.ArchivedAt.Valid {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "target Chat Agent is archived"}
	}
	if !targetAgent.RuntimeID.Valid {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "target Chat Agent has no runtime"}
	}
	originator := sourceTaskOriginator(sourceTask)
	if !originator.Valid {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "source task has no human originator"}
	}
	if !h.canInvokeAgent(r.Context(), targetAgent, "agent", sourceAgentID, uuidToString(originator), workspaceID) {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "source task originator cannot invoke the target Chat Agent"}
	}

	provenanceContext, err := json.Marshal(map[string]string{
		multicaMCPForwardedFromTaskContextKey:    sourceTaskID,
		multicaMCPForwardedFromSessionContextKey: uuidToString(sourceTask.ChatSessionID),
		multicaMCPForwardedFromAgentContextKey:   sourceAgentID,
	})
	if err != nil {
		return multicaMCPChatSendResult{}, err
	}
	trace := chattrace.New("mcp")
	sent, err := h.TaskService.SendDirectChatMessageWithContext(
		r.Context(),
		targetSession,
		targetAgent,
		originator,
		args.Content,
		nil,
		"agent",
		sourceTask.AgentID,
		provenanceContext,
		trace,
	)
	if err != nil {
		return multicaMCPChatSendResult{}, err
	}
	resolvedSessionID := uuidToString(targetSession.ID)
	h.publishChat(protocol.EventChatMessage, workspaceID, "agent", sourceAgentID, resolvedSessionID, protocol.ChatMessagePayload{
		ChatSessionID: resolvedSessionID,
		MessageID:     uuidToString(sent.Message.ID),
		Role:          "user",
		Content:       args.Content,
		TaskID:        uuidToString(sent.Task.ID),
		CreatedAt:     timestampToString(sent.Message.CreatedAt),
		TraceID:       trace.TraceID,
	})
	slog.Info("Multica MCP Chat continuation queued",
		"source_task_id", sourceTaskID,
		"source_chat_session_id", uuidToString(sourceTask.ChatSessionID),
		"target_chat_session_id", resolvedSessionID,
		"target_task_id", uuidToString(sent.Task.ID),
	)
	return multicaMCPChatSendResult{
		SessionID: resolvedSessionID,
		MessageID: uuidToString(sent.Message.ID),
		TaskID:    uuidToString(sent.Task.ID),
		TraceID:   trace.TraceID,
		CreatedAt: timestampToString(sent.Task.CreatedAt),
	}, nil
}

func (h *Handler) writeMulticaMCPResult(w http.ResponseWriter, id json.RawMessage, result any) {
	writeJSON(w, http.StatusOK, multicaMCPResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (h *Handler) writeMulticaMCPError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	writeJSON(w, http.StatusOK, multicaMCPResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &multicaMCPError{Code: code, Message: message},
	})
}
