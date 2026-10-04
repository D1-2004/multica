package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/contextcap"
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

const connectorListAttemptTimeout = 8 * time.Second

// An administrator's tools/list is read-only and safe to retry once after a
// transient network timeout. Keep this separate from task tools/call, which
// can have side effects and must never be retried by the relay.
func (h *Handler) connectorToolListWithRetry(ctx context.Context, c internalConnector, cursor string) (any, int, error) {
	for attempt := 1; attempt <= 2; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, connectorListAttemptTimeout)
		result, err := h.callInternalConnectorUpstreamRaw(attemptCtx, c, "tools/list", connectorRPCParams{Cursor: cursor})
		cancel()
		if err == nil {
			return result, attempt, nil
		}
		if ctx.Err() != nil {
			return nil, attempt, ctx.Err()
		}
		if attempt == 2 || !retryableConnectorListTimeout(err) {
			return nil, attempt, err
		}
	}
	return nil, 2, context.DeadlineExceeded
}

func retryableConnectorListTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

type connectorUpstreamStatusError struct{ Code int }

func (e connectorUpstreamStatusError) Error() string { return fmt.Sprintf("upstream HTTP %d", e.Code) }

type connectorUpstreamProtocolError struct{}

func (connectorUpstreamProtocolError) Error() string { return "invalid upstream MCP response" }

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
	// OAuth is set for a credential stored by an OAuth connect. Bearer then
	// mirrors its access token, so an older binary can still use an
	// unexpired token.
	OAuth *contextcap.OAuthToken `json:"oauth,omitempty"`
}

// connectorSecret returns the credential the relay sends for c: the
// task-resolved one (person > scene > workspace) when set, else the sealed
// workspace credential, else the environment fallback. auth_mode 'none'
// yields an empty secret. The OAuth part may be expired; callers that send
// it use freshConnectorToken.
func (h *Handler) connectorSecret(c internalConnector) (contextcap.Secret, error) {
	if c.AuthMode == "none" {
		return contextcap.Secret{}, nil
	}
	if c.bearerResolved {
		if c.resolvedBearer == "" || strings.ContainsAny(c.resolvedBearer, "\r\n\x00") {
			return contextcap.Secret{}, errors.New("connector credential unavailable")
		}
		return contextcap.Secret{Bearer: c.resolvedBearer, OAuth: c.resolvedOAuth}, nil
	}
	id, err := uuid.Parse(c.ID)
	if err != nil || id == uuid.Nil || c.CredentialRef != connectorCredentialRef(id.String()) {
		return contextcap.Secret{}, errors.New("invalid connector credential reference")
	}
	if len(c.CredentialCiphertext) > 0 {
		return h.openWorkspaceConnectorSecret(c.WorkspaceID, c.ID, c.CredentialCiphertext)
	}
	if !connectorCredentialReady(c) {
		return contextcap.Secret{}, errors.New("connector credential unavailable")
	}
	return contextcap.Secret{Bearer: os.Getenv(c.CredentialRef)}, nil
}

// openWorkspaceConnectorSecret opens a workspace connector credential and
// checks that it is bound to this workspace and connector.
func (h *Handler) openWorkspaceConnectorSecret(workspaceID, connectorID string, ciphertext []byte) (contextcap.Secret, error) {
	if h.InternalConnectorSecretBox == nil {
		return contextcap.Secret{}, errors.New("connector credential key unavailable")
	}
	plain, err := h.InternalConnectorSecretBox.Open(ciphertext)
	var sealed connectorSealedCredential
	if err != nil || json.Unmarshal(plain, &sealed) != nil || sealed.WorkspaceID != workspaceID || sealed.ConnectorID != connectorID {
		return contextcap.Secret{}, errors.New("connector credential unavailable")
	}
	secret, err := contextcap.OpenSecretPayload(sealed.Bearer, sealed.OAuth)
	if err != nil {
		return contextcap.Secret{}, errors.New("connector credential unavailable")
	}
	return secret, nil
}

// sealWorkspaceConnectorSecret seals a workspace credential bound to the
// workspace and connector.
func (h *Handler) sealWorkspaceConnectorSecret(workspaceID, connectorID string, secret contextcap.Secret) ([]byte, error) {
	if h.InternalConnectorSecretBox == nil {
		return nil, errors.New("connector credential key unavailable")
	}
	if secret.OAuth != nil {
		if !secret.OAuth.Valid() || secret.OAuth.AccessToken != secret.Bearer {
			return nil, errors.New("invalid OAuth credential")
		}
	} else if !validInternalConnectorBearer(secret.Bearer) {
		return nil, errors.New("invalid connector credential")
	}
	payload, err := json.Marshal(connectorSealedCredential{WorkspaceID: workspaceID, ConnectorID: connectorID, Bearer: secret.Bearer, OAuth: secret.OAuth})
	if err != nil {
		return nil, err
	}
	return h.InternalConnectorSecretBox.Seal(payload)
}

// connectorBearer returns a usable Bearer for c ("" for auth_mode 'none').
// An expired OAuth token without a refresh token is not usable.
func (h *Handler) connectorBearer(c internalConnector) (string, error) {
	secret, err := h.connectorSecret(c)
	if err != nil {
		return "", err
	}
	if c.AuthMode == "none" {
		return "", nil
	}
	if !secret.Usable(time.Now()) {
		return "", errors.New("connector credential expired")
	}
	return secret.Bearer, nil
}

// connectorCredentialAccount is the display hint of the workspace
// credential: "@<account>" or "OAuth" for an OAuth credential, "" otherwise.
func (h *Handler) connectorCredentialAccount(c internalConnector) string {
	if c.AuthMode == "none" || len(c.CredentialCiphertext) == 0 {
		return ""
	}
	secret, err := h.openWorkspaceConnectorSecret(c.WorkspaceID, c.ID, c.CredentialCiphertext)
	if err != nil || secret.OAuth == nil {
		return ""
	}
	return contextcap.OAuthHint(secret.OAuth.Account)
}

func (h *Handler) connectorCredentialReady(c internalConnector) bool {
	_, err := h.connectorBearer(c)
	return err == nil
}

func (h *Handler) connectorCredentialSource(c internalConnector) string {
	source, _ := h.connectorWorkspaceCredential(c)
	return source
}

// connectorWorkspaceCredential classifies the workspace-level credential of
// c: source "workspace" for a usable stored credential (returned, so its
// hint can be shown), "unavailable" for a stored one that cannot be opened
// or is no longer usable, "environment" for the operator-managed
// MULTICA_INTERNAL_MCP_BEARER_<id> fallback, and "none" for auth_mode 'none'
// or no credential at all.
func (h *Handler) connectorWorkspaceCredential(c internalConnector) (string, contextcap.Secret) {
	if c.AuthMode == "none" {
		return "none", contextcap.Secret{}
	}
	if len(c.CredentialCiphertext) > 0 {
		secret, err := h.connectorSecret(c)
		if err != nil || !secret.Usable(time.Now()) {
			return "unavailable", contextcap.Secret{}
		}
		return "workspace", secret
	}
	if connectorCredentialReady(c) {
		return "environment", contextcap.Secret{}
	}
	return "none", contextcap.Secret{}
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
  (connector_id,workspace_id,agent_id,task_id,method,tool_name,outcome,binding_layer,credential_layer)
  VALUES ($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,$8,$9)`, c.ID, c.WorkspaceID, agent, task, method, tool, outcome, c.bindingLayer, c.credentialLayer)
	if err == nil {
		slog.InfoContext(r.Context(), "internal connector call", "event", "internal_mcp_connector_call", "connector_id", c.ID, "workspace_id", c.WorkspaceID, "agent_id", agent, "task_id", task, "method", method, "tool_name", tool, "outcome", outcome, "binding_layer", c.bindingLayer, "credential_layer", c.credentialLayer)
	}
	return err
}

func (h *Handler) CallInternalConnector(w http.ResponseWriter, r *http.Request) {
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
	// Re-resolve on every call with the task's own scope, so a scene or
	// personal toggle, offer removal or credential revoke applies to the next
	// tool call of a running task.
	connectors, err := h.authorizedTaskConnectors(r.Context(), wsUUID, active)
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
	diagnostic := request.Method == "tools/call" && params.Name == connectorDiscoveryStatusTool
	if request.Method == "tools/call" {
		if params.Name == "" || !internalMCPJSONObject(params.Arguments) {
			h.writeMulticaMCPError(w, request.ID, -32602, "tool name or arguments are invalid")
			return
		}
		if diagnostic {
			if _, valid := connectorDiscoveryStatusResult(*c, params.Arguments); !valid {
				h.writeMulticaMCPError(w, request.ID, -32602, "diagnostic accepts only empty object arguments")
				return
			}
		} else if c.CatalogSlug != "" {
			// External official apps keep their pinned tools (read-only unless
			// writes are enabled). delete_branch is implemented here because
			// the upstream GitHub MCP server does not offer it.
			original, allowed := connectorOriginalTool(c.AllowedTools, params.Name)
			if !allowed && !githubLocalTool(c.CatalogSlug, params.Name, c.WriteEnabled) {
				h.writeMulticaMCPError(w, request.ID, -32602, "tool is not allowed")
				return
			}
			if allowed {
				params.Name = original
			}
		} else if strings.HasPrefix(params.Name, "t_") && len(params.Name) == 18 {
			// Resolve compact aliases against live metadata, never a saved allowlist.
			names, listErr := h.discoverInternalConnectorTools(r.Context(), *c)
			if listErr != nil {
				h.writeMulticaMCPError(w, request.ID, -32603, "upstream tool names unavailable")
				return
			}
			if original, found := connectorOriginalTool(names, params.Name); found {
				params.Name = original
			}
		}
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
	if diagnostic {
		if err = h.connectorAudit(r, *c, task, agent, request.Method, params.Name, "discovery_diagnostic"); err != nil {
			h.writeMulticaMCPError(w, request.ID, -32603, "connector audit unavailable")
			return
		}
		result, _ := connectorDiscoveryStatusResult(*c, params.Arguments)
		h.writeMulticaMCPResult(w, request.ID, result)
		return
	}
	denied := ""
	if request.Method == "tools/call" && c.CatalogSlug == "github" {
		access := h.githubGrantView(r.Context(), c)
		denied = githubOwnerBlockReason(params.Name, params.Arguments, access)
		if denied == "" {
			denied = githubToolBlockReason(params.Name, access)
		}
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
	var result any
	upstreamStarted := time.Now()
	agentRefusal := false
	if denied != "" {
		// Leave isError false. pi-mcp-extension discards isError text.
		result = githubAgentRefusal(denied)
		err = nil
		agentRefusal = true
	} else if request.Method == "tools/call" && params.Name == "delete_branch" && c.CatalogSlug == "github" {
		var refusal bool
		result, refusal = h.githubDeleteBranchCall(r.Context(), c, params.Arguments)
		err = nil
		agentRefusal = refusal
	} else {
		result, err = h.callInternalConnectorUpstream(r.Context(), *c, request.Method, params)
	}
	if err != nil {
		class, status := connectorFailureClass(err)
		slog.WarnContext(r.Context(), "internal connector upstream failed", "event", "internal_mcp_connector_upstream_failed",
			"connector_id", c.ID, "workspace_id", ws, "agent_id", agent, "task_id", task,
			"method", request.Method, "failure_class", class, "upstream_status", status,
			"duration_ms", time.Since(upstreamStarted).Milliseconds(), "request_id", chimw.GetReqID(r.Context()),
			"binding_layer", c.bindingLayer, "credential_layer", c.credentialLayer)
	}
	var discoveryResult any
	discoveryUnavailable := false
	if request.Method == "tools/list" {
		discoveryResult, discoveryUnavailable = connectorUnavailableDiscovery(r.Context(), *c, params.Cursor, err)
	}
	outcome := "ok"
	failure := "Internal MCP tool list unavailable"
	if errors.Is(err, errConnectorReconnectRequired) {
		// The account behind this credential was disconnected or its grant
		// was revoked; tell the agent to have the user reconnect.
		outcome = "reconnect_required"
		failure = connectorReconnectMessage(*c)
		result = multicaMCPToolResult{IsError: true, Content: []multicaMCPContent{{Type: "text", Text: failure}}}
	} else if err != nil {
		outcome = "upstream_error"
		result = multicaMCPToolResult{IsError: true, Content: []multicaMCPContent{{Type: "text", Text: "Internal MCP upstream unavailable"}}}
	}
	if c.CatalogSlug == "github" && request.Method == "tools/call" && err == nil {
		if toolResult, ok := result.(multicaMCPToolResult); ok && toolResult.IsError && githubUpstreamPermissionFailure(toolResult) {
			if message := githubUpstreamPermissionMessage(params.Name, githubArgumentOwner(params.Arguments)); message != "" {
				result = githubAgentRefusal(message)
				agentRefusal = true
			}
		}
	}
	if agentRefusal {
		outcome = "tool_error"
	} else if toolResult, ok := result.(multicaMCPToolResult); ok && toolResult.IsError && err == nil {
		outcome = "tool_error"
	}
	if auditErr := h.connectorAudit(r, *c, task, agent, request.Method, params.Name, outcome); auditErr != nil {
		result = multicaMCPToolResult{IsError: true, Content: []multicaMCPContent{{Type: "text", Text: "connector audit unavailable"}}}
		outcome = "audit_error"
		failure = "Internal MCP tool list unavailable"
		discoveryUnavailable = false
	}
	if request.Method == "tools/list" && outcome != "ok" {
		if discoveryUnavailable {
			slog.WarnContext(r.Context(), "internal connector discovery presented as unavailable", "event", "internal_mcp_connector_discovery_unavailable",
				"connector_id", c.ID, "task_id", task, "binding_layer", c.bindingLayer, "credential_layer", c.credentialLayer)
			_ = json.NewEncoder(w).Encode(multicaMCPResponse{JSONRPC: "2.0", ID: request.ID, Result: discoveryResult})
			return
		}
		_ = json.NewEncoder(w).Encode(multicaMCPResponse{JSONRPC: "2.0", ID: request.ID, Error: &multicaMCPError{Code: -32603, Message: failure}})
		return
	}
	_ = json.NewEncoder(w).Encode(multicaMCPResponse{JSONRPC: "2.0", ID: request.ID, Result: result})
}

func (h *Handler) callInternalConnectorUpstream(ctx context.Context, c internalConnector, method string, params connectorRPCParams) (any, error) {
	result, err := h.callInternalConnectorUpstreamRaw(ctx, c, method, params)
	if err != nil || method != "tools/list" {
		return result, err
	}
	list := result.(map[string]any)
	if c.CatalogSlug != "" {
		// External official apps keep their separate write opt-in contract.
		allowed := map[string]bool{}
		for _, name := range c.AllowedTools {
			allowed[name] = true
		}
		tools := []map[string]any{}
		for _, item := range list["tools"].([]map[string]any) {
			name, _ := item["name"].(string)
			if allowed[name] {
				tools = append(tools, item)
			}
		}
		if c.CatalogSlug == "github" {
			access := h.githubGrantView(ctx, &c)
			tools = githubPresentTools(tools, access, c.WriteEnabled)
		}
		list["tools"] = tools
	}
	seen := map[string]bool{}
	for _, item := range list["tools"].([]map[string]any) {
		name, _ := item["name"].(string)
		presented := connectorPresentedToolName(name)
		if name == "" || name == connectorDiscoveryStatusTool || seen[presented] {
			return nil, connectorUpstreamProtocolError{}
		}
		seen[presented] = true
		if presented != name {
			item["name"] = presented
			description, _ := item["description"].(string)
			item["description"] = "Upstream tool " + name + ". " + description
		}
	}
	return list, nil
}

// Raw discovery preserves every upstream definition. Presentation only aliases
// long names for the runtime's name limit; it never filters tools.
func (h *Handler) callInternalConnectorUpstreamRaw(ctx context.Context, c internalConnector, method string, params connectorRPCParams) (any, error) {
	upstreamParams := map[string]any{}
	if method == "tools/list" && params.Cursor != "" {
		upstreamParams["cursor"] = params.Cursor
	}
	if method == "tools/call" {
		upstreamParams["name"] = params.Name
		upstreamParams["arguments"] = params.Arguments
	}
	var result json.RawMessage
	var err error
	if c.CatalogSlug != "" {
		// Official apps: proxy-aware, host-pinned, session-aware client with
		// OAuth refresh (internal_connector_oauth_refresh.go).
		result, err = h.callCatalogConnector(ctx, &c, method, upstreamParams)
	} else {
		result, err = h.postInternalConnectorRPC(ctx, c, method, upstreamParams)
	}
	if err != nil {
		return nil, err
	}
	return connectorRPCResult(method, result)
}

// postInternalConnectorRPC sends one JSON-RPC request to a custom (Aone
// FaaS) connector through InternalConnectorClient and returns its result.
func (h *Handler) postInternalConnectorRPC(ctx context.Context, c internalConnector, method string, upstreamParams map[string]any) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": upstreamParams})
	if err != nil {
		return nil, err
	}
	bearer, err := h.connectorBearer(c)
	if err != nil {
		return nil, err
	}
	upstreamURL := c.UpstreamURL
	// Stored capability addresses omit the encrypted token. Reconstruct it only
	// at the final HTTP boundary so older remote deployments also work.
	if strings.HasSuffix(upstreamURL, "/api/mcp/connect") {
		if bearer == "" {
			return nil, errors.New("connector capability credential unavailable")
		}
		upstreamURL += "/" + url.PathEscape(bearer)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid connector request URL")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", multicaMCPProtocolVersion)
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
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
		return nil, connectorUpstreamStatusError{Code: response.StatusCode}
	}
	raw, err := readInternalMCPResponse(response.Body, response.Header.Get("Content-Type"))
	if err != nil {
		return nil, connectorUpstreamProtocolError{}
	}
	var rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &rpc) != nil || rpc.JSONRPC != "2.0" || string(rpc.ID) != "1" || (len(rpc.Error) > 0 && string(rpc.Error) != "null") {
		return nil, connectorUpstreamProtocolError{}
	}
	return rpc.Result, nil
}

// connectorRPCResult preserves raw tool lists and bounds tool errors.
func connectorRPCResult(method string, result json.RawMessage) (any, error) {
	if method == "tools/list" {
		var list struct {
			Tools      []map[string]any `json:"tools"`
			NextCursor string           `json:"nextCursor"`
		}
		if json.Unmarshal(result, &list) != nil || list.Tools == nil {
			return nil, connectorUpstreamProtocolError{}
		}
		for _, tool := range list.Tools {
			if tool["name"] == connectorDiscoveryStatusTool {
				return nil, connectorUpstreamProtocolError{}
			}
		}
		value := map[string]any{"tools": list.Tools}
		if list.NextCursor != "" {
			value["nextCursor"] = list.NextCursor
		}
		return value, nil
	}
	var toolResult multicaMCPToolResult
	if json.Unmarshal(result, &toolResult) != nil || len(toolResult.Content) == 0 {
		return toolResult, errors.New("invalid upstream tool result")
	}
	if toolResult.IsError {
		const maxToolErrorRunes = 4096
		content := make([]multicaMCPContent, 0, len(toolResult.Content))
		remaining := maxToolErrorRunes
		for _, item := range toolResult.Content {
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
	return toolResult, nil
}
