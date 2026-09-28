package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/redis/go-redis/v9"
)

type internalConnectorRedis interface {
	Eval(context.Context, string, []string, ...interface{}) *redis.Cmd
}

type connectorRPCParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Cursor    string          `json:"cursor"`
}

func NewInternalConnectorClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{Timeout: 45 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

const connectorIncrementWithTTL = `local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('PEXPIRE', KEYS[1], 120000) end
return n`

func connectorCredentialReady(c internalConnector) bool {
	id, err := uuid.Parse(c.ID)
	if err != nil || id == uuid.Nil || c.CredentialRef != connectorCredentialRef(id.String()) {
		return false
	}
	token := os.Getenv(c.CredentialRef)
	return token != "" && !strings.ContainsAny(token, "\r\n")
}

type connectorSealedCredential struct {
	WorkspaceID string `json:"workspace_id"`
	ConnectorID string `json:"connector_id"`
	Bearer      string `json:"bearer"`
}

func (h *Handler) connectorBearer(c internalConnector) (string, error) {
	id, err := uuid.Parse(c.ID)
	if err != nil || id == uuid.Nil || c.CredentialRef != connectorCredentialRef(id.String()) {
		return "", errors.New("invalid connector credential reference")
	}
	if len(c.CredentialCiphertext) > 0 {
		if h.InternalConnectorSecretBox == nil {
			return "", errors.New("connector credential key unavailable")
		}
		plain, err := h.InternalConnectorSecretBox.Open(c.CredentialCiphertext)
		var sealed connectorSealedCredential
		if err != nil || json.Unmarshal(plain, &sealed) != nil || sealed.WorkspaceID != c.WorkspaceID || sealed.ConnectorID != c.ID ||
			sealed.Bearer == "" || strings.ContainsAny(sealed.Bearer, "\r\n\x00") {
			return "", errors.New("connector credential unavailable")
		}
		return sealed.Bearer, nil
	}
	if !connectorCredentialReady(c) {
		return "", errors.New("connector credential unavailable")
	}
	return os.Getenv(c.CredentialRef), nil
}

func (h *Handler) connectorCredentialReady(c internalConnector) bool {
	_, err := h.connectorBearer(c)
	return err == nil
}

func (h *Handler) connectorCredentialSource(c internalConnector) string {
	if len(c.CredentialCiphertext) > 0 {
		if h.connectorCredentialReady(c) {
			return "workspace"
		}
		return "unavailable"
	}
	if connectorCredentialReady(c) {
		return "environment"
	}
	return "none"
}

func (h *Handler) connectorLimit(ctx context.Context, connectorID, task, agent, ws string) error {
	if h.InternalConnectorRedis == nil {
		return errors.New("shared rate limiter is unavailable")
	}
	prefix := "mcpconn:" + featureflags.DeploymentEnvironment() + ":connector:" + connectorID + ":rate:"
	for _, scope := range []struct {
		label, id string
		max       int64
	}{{"task", task, 30}, {"agent", agent, 120}, {"workspace", ws, 600}} {
		key := prefix + scope.label + ":" + scope.id + ":" + time.Now().UTC().Format("200601021504")
		n, err := h.InternalConnectorRedis.Eval(ctx, connectorIncrementWithTTL, []string{key}).Int64()
		if err != nil {
			return err
		}
		if n > scope.max {
			return errors.New("connector rate limit exceeded")
		}
	}
	return nil
}

func (h *Handler) connectorAudit(r *http.Request, c internalConnector, task, agent, method, tool, outcome string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
	defer cancel()
	_, err := h.DB.Exec(ctx, `INSERT INTO internal_connector_call_audit
  (connector_id,workspace_id,agent_id,task_id,method,tool_name,outcome)
  VALUES ($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7)`, c.ID, c.WorkspaceID, agent, task, method, tool, outcome)
	if err == nil {
		slog.InfoContext(r.Context(), "internal connector call", "event", "internal_mcp_connector_call", "connector_id", c.ID, "workspace_id", c.WorkspaceID, "agent_id", agent, "task_id", task, "method", method, "tool_name", tool, "outcome", outcome)
	}
	return err
}

func (h *Handler) CallInternalConnector(w http.ResponseWriter, r *http.Request) {
	if !h.internalConnectorsEnabled(r.Context()) {
		http.NotFound(w, r)
		return
	}
	if !multicaMCPTaskTokenAuthenticated(r) || !h.multicaMCPOriginAllowed(r) {
		writeError(w, http.StatusForbidden, "task token required")
		return
	}
	ws, agent, task := r.Header.Get("X-Workspace-ID"), r.Header.Get("X-Agent-ID"), r.Header.Get("X-Task-ID")
	connectorID := chi.URLParam(r, "connectorId")
	if parsed, err := uuid.Parse(connectorID); err != nil || parsed.String() != connectorID {
		http.NotFound(w, r)
		return
	}
	taskUUID, err := util.ParseUUID(task)
	if err != nil {
		writeError(w, 403, "invalid task")
		return
	}
	wsUUID, err := util.ParseUUID(ws)
	if err != nil {
		writeError(w, 403, "invalid workspace")
		return
	}
	active, err := h.Queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{ID: taskUUID, WorkspaceID: wsUUID})
	if err != nil || util.UUIDToString(active.AgentID) != agent || !slices.Contains([]string{"running", "dispatched"}, active.Status) {
		writeError(w, 403, "task is not active or authorized")
		return
	}
	connectors, err := h.authorizedConnectors(r.Context(), ws, agent)
	if err != nil {
		writeError(w, 503, "connector store unavailable")
		return
	}
	var c *internalConnector
	for i := range connectors {
		if connectors[i].ID == connectorID {
			c = &connectors[i]
			break
		}
	}
	if c == nil {
		writeError(w, 403, "connector is not authorized")
		return
	}
	if err = validateConnectorInput(connectorInput{Name: c.Name, UpstreamURL: c.UpstreamURL, AllowedTools: c.AllowedTools, AgentIDs: []string{agent}, Enabled: true}); err != nil {
		writeError(w, 503, "connector configuration unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, 413, "request too large")
		return
	}
	var request multicaMCPRequest
	if json.Unmarshal(body, &request) != nil || request.JSONRPC != "2.0" {
		h.writeMulticaMCPError(w, request.ID, -32600, "invalid MCP request")
		return
	}
	if request.Method == "notifications/initialized" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if len(request.ID) == 0 || string(request.ID) == "null" {
		h.writeMulticaMCPError(w, request.ID, -32600, "MCP request ID required")
		return
	}
	if !slices.Contains([]string{"initialize", "tools/list", "tools/call"}, request.Method) {
		h.writeMulticaMCPError(w, request.ID, -32601, "method not found")
		return
	}
	if request.Method == "initialize" {
		h.writeMulticaMCPResult(w, request.ID, map[string]any{"protocolVersion": multicaMCPProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": c.Name, "version": "1.0.0"}})
		return
	}
	var params connectorRPCParams
	if len(request.Params) > 0 && json.Unmarshal(request.Params, &params) != nil {
		h.writeMulticaMCPError(w, request.ID, -32602, "invalid MCP parameters")
		return
	}
	if request.Method == "tools/call" && (!slices.Contains(c.AllowedTools, params.Name) || !internalMCPJSONObject(params.Arguments)) {
		h.writeMulticaMCPError(w, request.ID, -32602, "tool is not allowed or arguments are invalid")
		return
	}
	if request.Method == "tools/list" && (params.Name != "" || len(params.Arguments) > 0) {
		h.writeMulticaMCPError(w, request.ID, -32602, "invalid tools/list parameters")
		return
	}
	if err = h.connectorLimit(r.Context(), c.ID, task, agent, ws); err != nil {
		_ = h.connectorAudit(r, *c, task, agent, request.Method, params.Name, "rate_limited")
		h.writeMulticaMCPToolError(w, request.ID, "connector rate limit or store unavailable")
		return
	}
	if err = h.connectorAudit(r, *c, task, agent, request.Method, params.Name, "forwarded"); err != nil {
		h.writeMulticaMCPToolError(w, request.ID, "connector audit unavailable")
		return
	}
	// Complete authorization and audit before releasing response headers. The
	// public sandbox relay otherwise cancels a long tool call after 30 seconds.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_ = http.NewResponseController(w).Flush()
	result, err := h.callInternalConnectorUpstream(r.Context(), *c, request.Method, params)
	outcome := "ok"
	if err != nil {
		outcome = "upstream_error"
		result = multicaMCPToolResult{IsError: true, Content: []multicaMCPContent{{Type: "text", Text: "Internal MCP upstream unavailable"}}}
	}
	if toolResult, ok := result.(multicaMCPToolResult); ok && toolResult.IsError && err == nil {
		outcome = "tool_error"
	}
	if auditErr := h.connectorAudit(r, *c, task, agent, request.Method, params.Name, outcome); auditErr != nil {
		result = multicaMCPToolResult{IsError: true, Content: []multicaMCPContent{{Type: "text", Text: "connector audit unavailable"}}}
		outcome = "audit_error"
	}
	if request.Method == "tools/list" && outcome != "ok" {
		_ = json.NewEncoder(w).Encode(multicaMCPResponse{JSONRPC: "2.0", ID: request.ID, Error: &multicaMCPError{Code: -32603, Message: "Internal MCP tool list unavailable"}})
		return
	}
	_ = json.NewEncoder(w).Encode(multicaMCPResponse{JSONRPC: "2.0", ID: request.ID, Result: result})
}

func (h *Handler) callInternalConnectorUpstream(ctx context.Context, c internalConnector, method string, params connectorRPCParams) (any, error) {
	upstreamParams := map[string]any{}
	if method == "tools/list" && params.Cursor != "" {
		upstreamParams["cursor"] = params.Cursor
	}
	if method == "tools/call" {
		upstreamParams["name"] = params.Name
		upstreamParams["arguments"] = params.Arguments
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": upstreamParams})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.UpstreamURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	bearer, err := h.connectorBearer(c)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	client := h.InternalConnectorClient
	if client == nil {
		return nil, errors.New("internal connector HTTP client unavailable")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("upstream HTTP error")
	}
	raw, err := readInternalMCPResponse(response.Body, response.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	var rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &rpc) != nil || rpc.JSONRPC != "2.0" || string(rpc.ID) != "1" || (len(rpc.Error) > 0 && string(rpc.Error) != "null") {
		return nil, errors.New("invalid upstream MCP response")
	}
	if method == "tools/list" {
		var list struct {
			Tools      []map[string]any `json:"tools"`
			NextCursor string           `json:"nextCursor"`
		}
		if json.Unmarshal(rpc.Result, &list) != nil || list.Tools == nil {
			return nil, errors.New("invalid upstream tools/list")
		}
		allowed := make(map[string]bool, len(c.AllowedTools))
		for _, name := range c.AllowedTools {
			allowed[name] = true
		}
		tools := []map[string]any{}
		for _, item := range list.Tools {
			name, _ := item["name"].(string)
			if allowed[name] {
				tools = append(tools, item)
			}
		}
		value := map[string]any{"tools": tools}
		if list.NextCursor != "" {
			value["nextCursor"] = list.NextCursor
		}
		return value, nil
	}
	var result multicaMCPToolResult
	if json.Unmarshal(rpc.Result, &result) != nil || len(result.Content) == 0 {
		return result, errors.New("invalid upstream tool result")
	}
	if result.IsError {
		const maxToolErrorRunes = 4096
		content := make([]multicaMCPContent, 0, len(result.Content))
		remaining := maxToolErrorRunes
		for _, item := range result.Content {
			if item.Type != "text" || item.Text == "" || remaining == 0 {
				continue
			}
			runes := []rune(item.Text)
			if len(runes) > remaining {
				runes = runes[:remaining]
			}
			remaining -= len(runes)
			content = append(content, multicaMCPContent{Type: "text", Text: string(runes)})
		}
		if len(content) == 0 {
			content = []multicaMCPContent{{Type: "text", Text: "Internal MCP tool failed"}}
		}
		return multicaMCPToolResult{IsError: true, Content: content}, nil
	}
	return result, nil
}
