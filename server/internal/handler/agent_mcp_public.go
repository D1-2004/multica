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
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/auth"
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
	"github.com/multica-ai/multica/server/internal/util"
)

const (
	agentMCPDelegateTool      = "delegate_task"
	agentMCPGetTaskTool       = "get_task"
	agentMCPGetIssueTool      = "get_issue"
	agentMCPContinueIssueTool = "continue_issue"
	agentMCPListArtifactsTool = "list_artifacts"
	agentMCPReadArtifactTool  = "read_artifact"
	agentMCPDescribeAgentTool = "describe_agent"
)

type agentMCPGetTaskArguments struct {
	TaskID string `json:"task_id"`
}

// HandleAgentMCP exposes one hosted Agent as a Streamable HTTP MCP server.
// MCP credentials use the same revocable credential substrate as A2A,
// but the MCP link remains usable independently of A2A publication state.
// The /connect/{accessToken} route is an intentionally simple capability URL
// for clients such as Codex that cannot persist a literal HTTP auth header.
func (h *Handler) HandleAgentMCP(w http.ResponseWriter, r *http.Request) {
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
		OwnerID:               uuidToString(credential.DelegatedByUserID),
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
		"serverInfo":      map[string]string{"name": "multica-hosted-agent", "version": "1.1.0"},
		"instructions":    "Call describe_agent to inspect the hosted Agent. delegate_task creates a visible Multica Issue by default. Use continue_issue for follow-up work on the same Issue, and list_artifacts/read_artifact for files uploaded to the Issue.",
	})
}

func agentMCPToolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        agentMCPDelegateTool,
			"title":       "Create an Issue and delegate it to this Multica Agent",
			"description": "Create a visible Multica Issue assigned to this hosted Agent and start an asynchronous task. The default mode is issue. Use get_task with the returned task id until terminal. mode=direct preserves the legacy isolated task behavior.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"instruction":     map[string]any{"type": "string", "minLength": 1, "description": "The complete task instruction. In issue mode this becomes the Issue description."},
					"request_id":      map[string]any{"type": "string", "minLength": 1, "maxLength": 256, "description": "Optional stable idempotency key for safe retries."},
					"mode":            map[string]any{"type": "string", "enum": []string{"issue", "direct"}, "default": "issue", "description": "Use issue for visible, resumable work. direct is a legacy isolated task."},
					"title":           map[string]any{"type": "string", "minLength": 1, "maxLength": agentMCPTitleMaxRunes, "description": "Optional Issue title. Defaults to the first instruction line."},
					"priority":        map[string]any{"type": "string", "enum": validIssuePriorities, "default": "none"},
					"project_id":      map[string]any{"type": "string", "format": "uuid", "description": "Optional same-workspace project UUID."},
					"parent_issue_id": map[string]any{"type": "string", "format": "uuid", "description": "Optional same-workspace parent Issue UUID."},
					"stage":           map[string]any{"type": "integer", "minimum": 1, "description": "Optional ordered stage under the parent Issue."},
					"start_date":      map[string]any{"type": "string", "format": "date", "description": "Optional YYYY-MM-DD start date."},
					"due_date":        map[string]any{"type": "string", "format": "date", "description": "Optional YYYY-MM-DD due date."},
					"attachment_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "string", "format": "uuid"}, "uniqueItems": true, "description": "Optional IDs of already uploaded same-workspace attachments, matching native Issue creation."},
					"allow_duplicate": map[string]any{"type": "boolean", "default": false, "description": "Allow another active Issue with the same title/project/parent. Matches native Issue duplicate protection."},
				},
				"required": []string{"instruction"},
			},
			"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": true},
		},
		{
			"name":        agentMCPGetTaskTool,
			"title":       "Get a delegated Multica task",
			"description": "Read the current execution state, Issue reference, final text, and persistent Issue resource metadata of a task previously created through this connection.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"task_id": map[string]any{"type": "string", "minLength": 1}},
				"required":   []string{"task_id"},
			},
			"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		},
		{
			"name":        agentMCPGetIssueTool,
			"title":       "Get a delegated Multica Issue",
			"description": "Read a visible Issue created through this MCP connection, including its MCP task history and comment timeline.",
			"inputSchema": agentMCPIssueIDSchema(),
			"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		},
		{
			"name":        agentMCPContinueIssueTool,
			"title":       "Continue work on a delegated Issue",
			"description": "Add a follow-up comment to an MCP-created Issue and start another Agent task on the same Issue. The previous session/workdir can be resumed by the runtime.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"issue_id":       map[string]any{"type": "string", "format": "uuid"},
					"instruction":    map[string]any{"type": "string", "minLength": 1},
					"request_id":     map[string]any{"type": "string", "minLength": 1, "maxLength": 256, "description": "Optional stable idempotency key for safe retries."},
					"attachment_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string", "format": "uuid"}, "uniqueItems": true, "description": "Optional IDs of already uploaded Issue attachments to bind to this follow-up."},
				},
				"required": []string{"issue_id", "instruction"},
			},
			"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": true},
		},
		{
			"name":        agentMCPListArtifactsTool,
			"title":       "List Issue artifacts",
			"description": "List persistent files uploaded to an MCP-created Issue or its comments. Sandbox filesystem paths are intentionally not exposed.",
			"inputSchema": agentMCPIssueIDSchema(),
			"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		},
		{
			"name":        agentMCPReadArtifactTool,
			"title":       "Read an Issue artifact",
			"description": "Read a persistent Issue attachment as an MCP embedded resource. Text is UTF-8; binary data is base64. Inline size is limited to 2 MiB.",
			"inputSchema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"artifact_id": map[string]any{"type": "string", "format": "uuid"}},
				"required":   []string{"artifact_id"},
			},
			"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		},
		{
			"name":        agentMCPDescribeAgentTool,
			"title":       "Describe this hosted Agent",
			"description": "Return the Agent's public name, description, version, declared skills, and the supported Issue-backed workflow.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}},
			"annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		},
	}
}

func agentMCPIssueIDSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"issue_id": map[string]any{"type": "string", "format": "uuid"}},
		"required":   []string{"issue_id"},
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
		var args agentMCPIssueDelegateArguments
		if decodeAgentMCPArguments(params.Arguments, &args) != nil || validateAgentMCPIssueArguments(&args) != nil {
			h.writeMulticaMCPError(w, req.ID, -32602, "invalid delegate_task arguments")
			return
		}
		if args.Mode == "issue" {
			result, err = h.delegateAgentMCPIssue(r.Context(), args, principal)
		} else {
			requestID := strings.TrimSpace(args.RequestID)
			if requestID == "" {
				requestID = a2a.NewMessageID()
			}
			result, err = h.A2AService.SendMessage(r.Context(), &a2a.SendMessageRequest{
				Config:  &a2a.SendMessageConfig{ReturnImmediately: true, AcceptedOutputModes: []string{"text/plain"}},
				Message: &a2a.Message{ID: requestID, Role: a2a.MessageRoleUser, Parts: a2a.ContentParts{a2a.NewTextPart(args.Instruction)}},
			})
		}
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
		ids, idsErr := parseAgentMCPPrincipalIDs(principal)
		if idsErr != nil {
			err = idsErr
			break
		}
		result, err = h.getAgentMCPIssueTask(r.Context(), strings.TrimSpace(args.TaskID), ids)
		if errors.Is(err, pgx.ErrNoRows) {
			result, err = h.A2AService.GetTask(r.Context(), &a2a.GetTaskRequest{ID: a2a.TaskID(strings.TrimSpace(args.TaskID))})
		}
	case agentMCPGetIssueTool:
		if !agentMCPHasScope(principal.Scopes, "read") {
			h.writeMulticaMCPToolError(w, req.ID, "this connection cannot read Issues")
			return
		}
		var args agentMCPIssueArguments
		if decodeAgentMCPArguments(params.Arguments, &args) != nil {
			h.writeMulticaMCPError(w, req.ID, -32602, "invalid get_issue arguments")
			return
		}
		ids, parseErr := parseAgentMCPPrincipalIDs(principal)
		issueID, issueErr := util.ParseUUID(strings.TrimSpace(args.IssueID))
		if parseErr != nil || issueErr != nil || !issueID.Valid {
			h.writeMulticaMCPError(w, req.ID, -32602, "invalid get_issue arguments")
			return
		}
		result, err = h.getAgentMCPIssue(r.Context(), issueID, ids)
	case agentMCPContinueIssueTool:
		if !agentMCPHasScope(principal.Scopes, "send") {
			h.writeMulticaMCPToolError(w, req.ID, "this connection cannot continue Issues")
			return
		}
		var args agentMCPContinueIssueArguments
		if decodeAgentMCPArguments(params.Arguments, &args) != nil {
			h.writeMulticaMCPError(w, req.ID, -32602, "invalid continue_issue arguments")
			return
		}
		result, err = h.continueAgentMCPIssue(r.Context(), args, principal)
	case agentMCPListArtifactsTool:
		if !agentMCPHasScope(principal.Scopes, "read") {
			h.writeMulticaMCPToolError(w, req.ID, "this connection cannot read artifacts")
			return
		}
		var args agentMCPIssueArguments
		if decodeAgentMCPArguments(params.Arguments, &args) != nil {
			h.writeMulticaMCPError(w, req.ID, -32602, "invalid list_artifacts arguments")
			return
		}
		ids, parseErr := parseAgentMCPPrincipalIDs(principal)
		issueID, issueErr := util.ParseUUID(strings.TrimSpace(args.IssueID))
		if parseErr != nil || issueErr != nil || !issueID.Valid {
			h.writeMulticaMCPError(w, req.ID, -32602, "invalid list_artifacts arguments")
			return
		}
		var artifacts []map[string]any
		artifacts, err = h.listAgentMCPIssueArtifacts(r.Context(), issueID, ids)
		result = map[string]any{"issue_id": args.IssueID, "artifacts": artifacts}
	case agentMCPReadArtifactTool:
		if !agentMCPHasScope(principal.Scopes, "read") {
			h.writeMulticaMCPToolError(w, req.ID, "this connection cannot read artifacts")
			return
		}
		var args agentMCPReadArtifactArguments
		if decodeAgentMCPArguments(params.Arguments, &args) != nil {
			h.writeMulticaMCPError(w, req.ID, -32602, "invalid read_artifact arguments")
			return
		}
		ids, parseErr := parseAgentMCPPrincipalIDs(principal)
		artifactID, artifactErr := util.ParseUUID(strings.TrimSpace(args.ArtifactID))
		if parseErr != nil || artifactErr != nil || !artifactID.Valid {
			h.writeMulticaMCPError(w, req.ID, -32602, "invalid read_artifact arguments")
			return
		}
		result, err = h.readAgentMCPArtifact(r.Context(), artifactID, ids)
	case agentMCPDescribeAgentTool:
		if !agentMCPHasScope(principal.Scopes, "read") {
			h.writeMulticaMCPToolError(w, req.ID, "this connection cannot read the Agent profile")
			return
		}
		var args struct{}
		if decodeAgentMCPArguments(params.Arguments, &args) != nil {
			h.writeMulticaMCPError(w, req.ID, -32602, "invalid describe_agent arguments")
			return
		}
		ids, parseErr := parseAgentMCPPrincipalIDs(principal)
		if parseErr != nil {
			err = parseErr
			break
		}
		result, err = h.describeAgentMCP(r.Context(), principal, ids)
	default:
		h.writeMulticaMCPError(w, req.ID, -32602, "unknown tool")
		return
	}
	if err != nil {
		slog.Info("hosted Agent MCP call rejected", "tool", params.Name, "public_agent_id", principal.PublicAgentID, "error", err)
		h.writeMulticaMCPToolError(w, req.ID, err.Error())
		return
	}
	if artifact, ok := result.(agentMCPReadArtifactResult); ok {
		h.writeMulticaMCPResult(w, req.ID, multicaMCPToolResult{
			Content:           []multicaMCPContent{{Type: "resource", Resource: &artifact.Resource}},
			StructuredContent: artifact.Metadata,
		})
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
