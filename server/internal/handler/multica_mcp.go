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
	multicaMCPAgentSearchTool       = "search_agents"
	multicaMCPAgentListTool         = "list_agents"
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

const multicaMCPAgentSearchToolDescription = `Search active Multica Agents by a case-insensitive name substring and return their detailed, non-secret metadata.

With a personal access token, the search spans every Workspace of the authenticated user but only returns Agents that user may view. With a task token, the search is limited to the authenticated task Workspace.

Workspace context is derived server-side. MCP configuration, environment values, runtime credentials, and other secret-bearing fields are never returned.`

const multicaMCPAgentListToolDescription = `List all active Multica Agents available to the authenticated caller and return their detailed, non-secret metadata.

With a personal access token, the result spans every Workspace of the authenticated user but only includes Agents that user may view. With a task token, the result is limited to the authenticated task Workspace.

Workspace context is derived server-side. MCP configuration, environment values, runtime credentials, and other secret-bearing fields are never returned.`

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

type multicaMCPAgentSearchArguments struct {
	Keyword string `json:"keyword"`
}

type multicaMCPAgentInfo struct {
	ID                 string                     `json:"id"`
	WorkspaceID        string                     `json:"workspace_id"`
	WorkspaceName      string                     `json:"workspace_name"`
	WorkspaceSlug      string                     `json:"workspace_slug"`
	Name               string                     `json:"name"`
	Description        string                     `json:"description"`
	Instructions       string                     `json:"instructions"`
	AvatarURL          *string                    `json:"avatar_url"`
	RuntimeID          string                     `json:"runtime_id"`
	RuntimeMode        string                     `json:"runtime_mode"`
	Status             string                     `json:"status"`
	PermissionMode     string                     `json:"permission_mode"`
	Visibility         string                     `json:"visibility"`
	InvocationTargets  []AgentInvocationTargetDTO `json:"invocation_targets"`
	OwnerID            *string                    `json:"owner_id"`
	MaxConcurrentTasks int32                      `json:"max_concurrent_tasks"`
	Model              string                     `json:"model"`
	ThinkingLevel      string                     `json:"thinking_level"`
	CreatedAt          string                     `json:"created_at"`
	UpdatedAt          string                     `json:"updated_at"`
}

type multicaMCPAgentQueryResult struct {
	Agents []multicaMCPAgentInfo `json:"agents"`
	Count  int                   `json:"count"`
}

type multicaMCPBindingTaskStore interface {
	GetAgentTaskInWorkspace(context.Context, db.GetAgentTaskInWorkspaceParams) (db.AgentTaskQueue, error)
}

type multicaMCPAgentStore interface {
	GetAgent(context.Context, pgtype.UUID) (db.Agent, error)
}

type multicaMCPWorkspaceMemberStore interface {
	GetMemberByUserAndWorkspace(context.Context, db.GetMemberByUserAndWorkspaceParams) (db.Member, error)
}

type multicaMCPAgentQueryStore interface {
	ListWorkspaces(context.Context, pgtype.UUID) ([]db.Workspace, error)
	GetWorkspace(context.Context, pgtype.UUID) (db.Workspace, error)
	ListAgents(context.Context, pgtype.UUID) ([]db.Agent, error)
	GetMemberByUserAndWorkspace(context.Context, db.GetMemberByUserAndWorkspaceParams) (db.Member, error)
	ListAgentInvocationTargetsByAgentIDs(context.Context, []pgtype.UUID) ([]db.AgentInvocationTarget, error)
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
	// The route is behind Auth. Auth has already validated the mul_ credential,
	// stripped any caller-supplied X-Actor-Source, and stamped X-User-ID before
	// this discriminator runs. Each write derives its workspace from the target
	// resource and enforces membership inside the business authorization gate.
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
		multicaMCPAgentSearchDefinition(),
		multicaMCPAgentListDefinition(),
	}
}

func multicaMCPAgentSearchDefinition() map[string]any {
	return map[string]any{
		"name":        multicaMCPAgentSearchTool,
		"title":       "Search Multica Agents by name",
		"description": multicaMCPAgentSearchToolDescription,
		"inputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"keyword": map[string]any{"type": "string", "minLength": 1, "description": "Case-insensitive substring to match against Agent names."},
			},
			"required": []string{"keyword"},
		},
		"outputSchema": multicaMCPAgentQueryOutputSchema(),
		"annotations": map[string]any{
			"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false,
		},
	}
}

func multicaMCPAgentListDefinition() map[string]any {
	return map[string]any{
		"name":        multicaMCPAgentListTool,
		"title":       "List Multica Agents",
		"description": multicaMCPAgentListToolDescription,
		"inputSchema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{},
		},
		"outputSchema": multicaMCPAgentQueryOutputSchema(),
		"annotations": map[string]any{
			"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false,
		},
	}
}

func multicaMCPAgentQueryOutputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agents": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"count":  map[string]any{"type": "integer", "minimum": 0},
		},
		"required": []string{"agents", "count"},
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
	if params.Name == multicaMCPAgentSearchTool || params.Name == multicaMCPAgentListTool {
		h.handleMulticaMCPAgentCall(w, r, req.ID, params.Name, params.Arguments)
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

func (h *Handler) handleMulticaMCPAgentCall(
	w http.ResponseWriter,
	r *http.Request,
	id json.RawMessage,
	toolName string,
	rawArguments json.RawMessage,
) {
	keyword := ""
	if toolName == multicaMCPAgentSearchTool {
		var args multicaMCPAgentSearchArguments
		if err := decodeMulticaMCPArguments(rawArguments, &args); err != nil {
			h.writeMulticaMCPError(w, id, -32602, "invalid search_agents arguments")
			return
		}
		keyword = strings.TrimSpace(args.Keyword)
		if keyword == "" {
			h.writeMulticaMCPError(w, id, -32602, "keyword is required")
			return
		}
	} else {
		var args struct{}
		if err := decodeMulticaMCPArguments(rawArguments, &args); err != nil {
			h.writeMulticaMCPError(w, id, -32602, "invalid list_agents arguments")
			return
		}
	}

	result, err := h.callMulticaMCPAgentQuery(r, keyword)
	if err != nil {
		var toolErr *multicaMCPToolCallError
		if !errors.As(err, &toolErr) {
			slog.Error("Multica MCP Agent query failed", "tool", toolName, "source_task_id", r.Header.Get("X-Task-ID"), "error", err)
			toolErr = &multicaMCPToolCallError{message: "failed to query Agents"}
		}
		h.writeMulticaMCPToolError(w, id, toolErr.message)
		return
	}
	payload, _ := json.Marshal(result)
	h.writeMulticaMCPResult(w, id, multicaMCPToolResult{
		Content:           []multicaMCPContent{{Type: "text", Text: string(payload)}},
		StructuredContent: result,
	})
}

func (h *Handler) callMulticaMCPAgentQuery(r *http.Request, keyword string) (multicaMCPAgentQueryResult, error) {
	store := h.multicaMCPAgents
	if store == nil {
		store = h.Queries
	}
	if store == nil {
		return multicaMCPAgentQueryResult{}, &multicaMCPToolCallError{message: "Agent query service is unavailable"}
	}

	type scopedWorkspace struct {
		workspace db.Workspace
		member    *db.Member
	}
	var scopes []scopedWorkspace
	if multicaMCPTaskTokenAuthenticated(r) {
		workspaceID, err := util.ParseUUID(strings.TrimSpace(r.Header.Get("X-Workspace-ID")))
		if err != nil {
			return multicaMCPAgentQueryResult{}, &multicaMCPToolCallError{message: "authenticated workspace is invalid"}
		}
		workspace, err := store.GetWorkspace(r.Context(), workspaceID)
		if errors.Is(err, pgx.ErrNoRows) {
			return multicaMCPAgentQueryResult{}, &multicaMCPToolCallError{message: "authenticated workspace was not found"}
		}
		if err != nil {
			return multicaMCPAgentQueryResult{}, err
		}
		scopes = append(scopes, scopedWorkspace{workspace: workspace})
	} else {
		userID, err := util.ParseUUID(strings.TrimSpace(r.Header.Get("X-User-ID")))
		if err != nil {
			return multicaMCPAgentQueryResult{}, &multicaMCPToolCallError{message: "authenticated user is invalid"}
		}
		workspaces, err := store.ListWorkspaces(r.Context(), userID)
		if err != nil {
			return multicaMCPAgentQueryResult{}, err
		}
		for _, workspace := range workspaces {
			member, err := store.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
				UserID: userID, WorkspaceID: workspace.ID,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return multicaMCPAgentQueryResult{}, err
			}
			scopes = append(scopes, scopedWorkspace{workspace: workspace, member: &member})
		}
	}

	needle := strings.ToLower(keyword)
	result := multicaMCPAgentQueryResult{Agents: []multicaMCPAgentInfo{}}
	for _, scope := range scopes {
		agents, err := store.ListAgents(r.Context(), scope.workspace.ID)
		if err != nil {
			return multicaMCPAgentQueryResult{}, err
		}
		targetsByAgent, err := multicaMCPAgentTargetsByAgent(r.Context(), store, agents)
		if err != nil {
			return multicaMCPAgentQueryResult{}, err
		}
		for _, agent := range agents {
			targets := targetsByAgent[uuidToString(agent.ID)]
			if scope.member != nil && !memberAllowedToViewAgent(agent, targets, strings.TrimSpace(r.Header.Get("X-User-ID")), scope.member.Role) {
				continue
			}
			if needle != "" && !strings.Contains(strings.ToLower(agent.Name), needle) {
				continue
			}
			result.Agents = append(result.Agents, multicaMCPAgentInfoFrom(agent, scope.workspace, targets))
		}
	}
	result.Count = len(result.Agents)
	return result, nil
}

func multicaMCPAgentTargetsByAgent(
	ctx context.Context,
	store multicaMCPAgentQueryStore,
	agents []db.Agent,
) (map[string][]db.AgentInvocationTarget, error) {
	ids := make([]pgtype.UUID, 0, len(agents))
	for _, agent := range agents {
		ids = append(ids, agent.ID)
	}
	result := make(map[string][]db.AgentInvocationTarget, len(agents))
	if len(ids) == 0 {
		return result, nil
	}
	targets, err := store.ListAgentInvocationTargetsByAgentIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, target := range targets {
		agentID := uuidToString(target.AgentID)
		result[agentID] = append(result[agentID], target)
	}
	return result, nil
}

func multicaMCPAgentInfoFrom(agent db.Agent, workspace db.Workspace, targets []db.AgentInvocationTarget) multicaMCPAgentInfo {
	invocationTargets := make([]AgentInvocationTargetDTO, 0, len(targets))
	for _, target := range targets {
		var targetID *string
		if target.TargetID.Valid {
			id := uuidToString(target.TargetID)
			targetID = &id
		}
		invocationTargets = append(invocationTargets, AgentInvocationTargetDTO{TargetType: target.TargetType, TargetID: targetID})
	}
	return multicaMCPAgentInfo{
		ID:                 uuidToString(agent.ID),
		WorkspaceID:        uuidToString(agent.WorkspaceID),
		WorkspaceName:      workspace.Name,
		WorkspaceSlug:      workspace.Slug,
		Name:               agent.Name,
		Description:        agent.Description,
		Instructions:       agent.Instructions,
		AvatarURL:          textToPtr(agent.AvatarUrl),
		RuntimeID:          uuidToString(agent.RuntimeID),
		RuntimeMode:        agent.RuntimeMode,
		Status:             agent.Status,
		PermissionMode:     agent.PermissionMode,
		Visibility:         deriveLegacyVisibility(agent.PermissionMode, targets),
		InvocationTargets:  invocationTargets,
		OwnerID:            uuidToPtr(agent.OwnerID),
		MaxConcurrentTasks: agent.MaxConcurrentTasks,
		Model:              agent.Model.String,
		ThinkingLevel:      agent.ThinkingLevel.String,
		CreatedAt:          timestampToString(agent.CreatedAt),
		UpdatedAt:          timestampToString(agent.UpdatedAt),
	}
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
	requestedAgentID = strings.TrimSpace(requestedAgentID)
	metadataStore := h.dingTalkAccountBindingMetadata
	if metadataStore == nil {
		metadataStore = h.Queries
	}
	if metadataStore == nil {
		return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: "Digital employee binding is not configured"}
	}
	var workspaceUUID pgtype.UUID
	var agentUUID pgtype.UUID
	var originator pgtype.UUID
	var agent db.Agent
	var err error
	if multicaMCPTaskTokenAuthenticated(r) {
		workspaceID := strings.TrimSpace(r.Header.Get("X-Workspace-ID"))
		workspaceUUID, err = util.ParseUUID(workspaceID)
		if err != nil {
			return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: "authenticated workspace is invalid"}
		}
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
		agentStore, ok := metadataStore.(multicaMCPAgentStore)
		if !ok {
			return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: "Digital employee binding is not configured"}
		}
		agent, err = agentStore.GetAgent(r.Context(), agentUUID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && agent.Kind != "user") {
			return multicaMCPBindingIdentity{}, &multicaMCPToolCallError{message: "authenticated Agent was not found"}
		}
		if err != nil {
			return multicaMCPBindingIdentity{}, err
		}
		workspaceUUID = agent.WorkspaceID
	}
	if !agent.ID.Valid {
		agent, err = metadataStore.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
			ID: agentUUID, WorkspaceID: workspaceUUID,
		})
	}
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

func validateMulticaMCPWorkspaceMember(
	ctx context.Context,
	store multicaMCPWorkspaceMemberStore,
	userID, workspaceID pgtype.UUID,
) error {
	if store == nil {
		return &multicaMCPToolCallError{message: "workspace membership service is unavailable"}
	}
	if _, err := store.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID: userID, WorkspaceID: workspaceID,
	}); err != nil {
		return &multicaMCPToolCallError{message: "authenticated user is not a member of the target workspace"}
	}
	return nil
}

func (h *Handler) callMulticaMCPChatSend(r *http.Request, args multicaMCPChatSendArguments) (multicaMCPChatSendResult, error) {
	if multicaMCPPersonalTokenAuthenticated(r) {
		return h.callMulticaMCPPersonalChatSend(r, args)
	}
	return h.callMulticaMCPTaskChatSend(r, args)
}

func (h *Handler) callMulticaMCPPersonalChatSend(r *http.Request, args multicaMCPChatSendArguments) (multicaMCPChatSendResult, error) {
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
	targetSession, err := h.Queries.GetChatSession(r.Context(), targetSessionUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "target Chat was not found"}
	}
	if err != nil {
		return multicaMCPChatSendResult{}, err
	}
	if targetSession.CreatorID != userUUID {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "target Chat is not owned by the authenticated user"}
	}
	workspaceUUID := targetSession.WorkspaceID
	if err := validateMulticaMCPWorkspaceMember(r.Context(), h.Queries, userUUID, workspaceUUID); err != nil {
		return multicaMCPChatSendResult{}, err
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
	taskOwnerUUID, err := util.ParseUUID(taskOwnerID)
	if err != nil {
		return multicaMCPChatSendResult{}, &multicaMCPToolCallError{message: "authenticated task owner is invalid"}
	}
	if err := validateMulticaMCPWorkspaceMember(r.Context(), h.Queries, taskOwnerUUID, workspaceUUID); err != nil {
		return multicaMCPChatSendResult{}, err
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
