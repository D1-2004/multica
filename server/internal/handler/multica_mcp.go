package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	multicaMCPProtocolVersion       = "2025-06-18"
	multicaMCPCompatProtocolVersion = "2025-03-26"
	multicaMCPChatSendTool          = "chat_send_message"
	multicaMCPServerName            = "multica"
	multicaMCPMaxRequestBytes       = 1 << 20
	multicaMCPForwardedFromTaskContextKey    = "mcp_forwarded_from_task_id"
	multicaMCPForwardedFromSessionContextKey = "mcp_forwarded_from_chat_session_id"
	multicaMCPForwardedFromAgentContextKey   = "mcp_forwarded_from_agent_id"
)

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

type multicaMCPToolCallError struct {
	message string
}

func (e *multicaMCPToolCallError) Error() string { return e.message }

// buildMulticaMCPConfig adds the server-owned Multica MCP entry to the
// canonical Claude-style MCP document consumed by every runtime adapter. The
// reserved `multica` name always wins, while unrelated servers and top-level
// provider settings survive unchanged.
//
// The task token is deliberately sent as an Authorization header. It must
// never be put in the endpoint URL because URLs routinely escape into access
// logs, browser history, and diagnostics.
func buildMulticaMCPConfig(existing json.RawMessage, publicURL, taskToken string) (json.RawMessage, bool, error) {
	publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	taskToken = strings.TrimSpace(taskToken)
	if publicURL == "" || !strings.HasPrefix(taskToken, "mat_") {
		return existing, false, nil
	}
	parsedURL, err := url.Parse(publicURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return existing, false, fmt.Errorf("invalid Multica public URL")
	}

	document := map[string]json.RawMessage{}
	trimmed := bytes.TrimSpace(existing)
	if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
		if err := json.Unmarshal(trimmed, &document); err != nil {
			return existing, false, fmt.Errorf("parse existing MCP config: %w", err)
		}
	}
	servers := map[string]json.RawMessage{}
	if rawServers, ok := document["mcpServers"]; ok && len(bytes.TrimSpace(rawServers)) > 0 {
		if err := json.Unmarshal(rawServers, &servers); err != nil {
			return existing, false, fmt.Errorf("parse existing mcpServers: %w", err)
		}
	}
	if servers == nil {
		servers = map[string]json.RawMessage{}
	}
	entry, err := json.Marshal(map[string]any{
		"type": "http",
		"url":  publicURL + "/api/mcp",
		"headers": map[string]string{
			"Authorization": "Bearer " + taskToken,
		},
	})
	if err != nil {
		return existing, false, err
	}
	servers[multicaMCPServerName] = entry
	serversJSON, err := json.Marshal(servers)
	if err != nil {
		return existing, false, err
	}
	document["mcpServers"] = serversJSON
	out, err := json.Marshal(document)
	if err != nil {
		return existing, false, err
	}
	return out, true, nil
}

func (h *Handler) multicaMCPChatSendEnabled(r *http.Request) bool {
	return h != nil && featureflags.MulticaMCPChatSendEnabled(r.Context(), h.FeatureFlags)
}

// injectMulticaMCPIntoClaim runs only after claim finalization, when the
// task-scoped token actually exists. Older/unsupported cloud images are left
// untouched; their immutable manifest is the server's compatibility signal.
func (h *Handler) injectMulticaMCPIntoClaim(r *http.Request, resp *AgentTaskResponse, runtime db.AgentRuntime, taskToken string) {
	if resp == nil || resp.Agent == nil || !h.multicaMCPChatSendEnabled(r) {
		return
	}
	if service.IsCloudSandboxRuntime(runtime) && !service.CloudSandboxRuntimeHasCapability(runtime, "mcp") {
		return
	}
	merged, injected, err := buildMulticaMCPConfig(resp.Agent.McpConfig, h.currentConfig().PublicURL, taskToken)
	if err != nil {
		slog.Warn("daemon claim: failed to inject Multica MCP config",
			"task_id", resp.ID,
			"runtime_id", uuidToString(runtime.ID),
			"error", err,
		)
		return
	}
	if injected {
		resp.Agent.McpConfig = merged
	}
}

// MulticaMCP is a stateless MCP Streamable HTTP endpoint. V1 does not expose
// server-initiated messages, so GET correctly reports 405 and every JSON-RPC
// exchange is completed by one POST response.
func (h *Handler) MulticaMCP(w http.ResponseWriter, r *http.Request) {
	if !h.multicaMCPChatSendEnabled(r) {
		writeError(w, http.StatusServiceUnavailable, "Multica MCP is not enabled")
		return
	}
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, http.StatusForbidden, "Multica MCP requires task-scoped authentication")
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
		h.writeMulticaMCPResult(w, req.ID, map[string]any{"tools": []any{multicaMCPChatSendDefinition()}})
	case "tools/call":
		h.handleMulticaMCPToolsCall(w, r, req)
	default:
		h.writeMulticaMCPError(w, req.ID, -32601, "method not found")
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
		"name":  multicaMCPChatSendTool,
		"title": "Continue an existing Multica Chat",
		"description": "Authorized server-provided Multica action: send a message to another existing Chat session owned by the authenticated task owner and queue that Chat's Agent to continue. This managed MCP tool is not a curl/raw-API workaround. Use it when the current Chat receives information that answers a question or unblocks work in another Chat. The target must be a different session_id.",
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

func (h *Handler) callMulticaMCPChatSend(r *http.Request, args multicaMCPChatSendArguments) (multicaMCPChatSendResult, error) {
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
