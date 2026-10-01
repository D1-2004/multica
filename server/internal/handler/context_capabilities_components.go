package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"

	"github.com/multica-ai/multica/server/internal/contextcap"
)

// Prompt components and custom MCP servers of one org, scene or person scope
// (docs/context-capabilities.md §1.3, §6): their views on the configure page,
// the configure page's PUT routes, and the writes the configure page and the
// admin Context Builder share. Who may change them is contextCapScopeRights
// (EditPrompts, EditMCP).

// contextCapErrInvalidMCPConfig: a configure-page custom MCP server
// configuration that is not remote-only or names a reserved server.
const contextCapErrInvalidMCPConfig = "invalid_mcp_config"

// contextCapPromptDTO is one prompt component of a scope on the configure
// page.
type contextCapPromptDTO struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Order int    `json:"order"`
	Text  string `json:"text"`
	// Enabled: the component takes part in the merge (a disabled one is
	// stored but neither applies nor overrides an outer one).
	Enabled bool `json:"enabled"`
}

// contextCapScopeComponentsDTO is a scope's own prompt components (ordered)
// and custom MCP servers on the configure page. MCPConfig is null when the
// scope has none, and withheld (MCPConfigRedacted) from a caller who may not
// edit it (its headers and URLs often carry tokens) or when the workspace
// always redacts secrets, as on the admin Context Builder node.
type contextCapScopeComponentsDTO struct {
	Prompts           []contextCapPromptDTO `json:"prompts"`
	MCPConfig         json.RawMessage       `json:"mcp_config"`
	MCPConfigRedacted bool                  `json:"mcp_config_redacted"`
}

// contextCapScopeComponents reads the prompt components and custom MCP
// servers of one scope of agent a under a.OrgID. rights are the caller's in
// that scope: the MCP servers are withheld without EditMCP.
func (h *Handler) contextCapScopeComponents(ctx context.Context, a contextCapAgent, scopeType, scopeKey string, rights contextCapRights) (contextCapScopeComponentsDTO, error) {
	out := contextCapScopeComponentsDTO{Prompts: []contextCapPromptDTO{}}
	prompts, err := contextcap.ListPromptComponents(ctx, h.DB, a.WorkspaceID, a.ID, scopeType, a.OrgID, scopeKey)
	if err != nil {
		return out, err
	}
	for _, prompt := range prompts {
		out.Prompts = append(out.Prompts, contextCapPromptDTO{
			ID: prompt.ID, Name: prompt.Name, Order: prompt.Order, Text: prompt.Text, Enabled: !prompt.Disabled,
		})
	}
	config, err := contextcap.GetScopeMCPConfig(ctx, h.DB, a.WorkspaceID, a.ID, scopeType, a.OrgID, scopeKey)
	if errors.Is(err, contextcap.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if !rights.EditMCP {
		out.MCPConfigRedacted = true
		return out, nil
	}
	redact, err := h.agentSceneRedactsMCPConfig(ctx, a.WorkspaceID)
	if err != nil {
		return out, err
	}
	if redact {
		out.MCPConfigRedacted = true
	} else {
		out.MCPConfig = config.MCPConfig
	}
	return out, nil
}

// contextPromptInput is one prompt component of a prompts PUT (the admin
// Context Builder and the configure page).
type contextPromptInput struct {
	Name  string `json:"name"`
	Order int    `json:"order"`
	Text  string `json:"text"`
	// Enabled is the component's switch. Omitted (a client older than the
	// switch) keeps an existing component's switch; a new one is enabled.
	Enabled *bool `json:"enabled"`
}

// contextPromptComponents validates a prompts PUT list
// (contextcap.NormalizePromptComponents) and writes the 400 otherwise:
// duplicate_prompt_name, or invalid_prompts.
func contextPromptComponents(w http.ResponseWriter, prompts []contextPromptInput) ([]contextcap.PromptComponentInput, bool) {
	components := make([]contextcap.PromptComponentInput, 0, len(prompts))
	for _, prompt := range prompts {
		components = append(components, contextcap.PromptComponentInput{
			Name: prompt.Name, Order: prompt.Order, Text: prompt.Text, Disabled: prompt.Enabled != nil && !*prompt.Enabled,
			KeepSwitch: prompt.Enabled == nil,
		})
	}
	if _, err := contextcap.NormalizePromptComponents(components); err != nil {
		if errors.Is(err, contextcap.ErrDuplicatePromptName) {
			writeErrorCode(w, http.StatusBadRequest, agentContextErrDuplicate, "two prompts have the same name")
			return nil, false
		}
		writeErrorCode(w, http.StatusBadRequest, agentContextErrPrompts,
			"at most 20 prompts, each with a name of 1-64 characters and a text of 1-8000 characters")
		return nil, false
	}
	return components, true
}

// agentContextPromptView is a stored prompt component with its update stamp.
func agentContextPromptView(prompt contextcap.PromptComponent) agentContextPromptDTO {
	return agentContextPromptDTO{
		ID: prompt.ID, Name: prompt.Name, Order: prompt.Order, Text: prompt.Text, Enabled: !prompt.Disabled,
		UpdatedByName: prompt.UpdatedByName, UpdatedAt: contextCapTime(prompt.UpdatedAt),
	}
}

// replaceContextPrompts replaces the prompt components of one scope in one
// transaction (contextcap.ReplacePromptComponents) and returns the stored
// list, or writes the error response and returns false.
func (h *Handler) replaceContextPrompts(w http.ResponseWriter, r *http.Request, write contextcap.PromptComponentsWrite) ([]agentContextPromptDTO, bool) {
	ctx := r.Context()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save the prompts")
		return nil, false
	}
	defer tx.Rollback(ctx)
	stored, err := contextcap.ReplacePromptComponents(ctx, tx, write)
	switch {
	case errors.Is(err, contextcap.ErrInvalidInput), errors.Is(err, contextcap.ErrNotFound):
		writeErrorCode(w, http.StatusBadRequest, agentContextErrPrompts, "invalid prompts")
		return nil, false
	case err != nil:
		slog.ErrorContext(ctx, "context builder: prompt write failed", "agent_id", write.AgentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save the prompts")
		return nil, false
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save the prompts")
		return nil, false
	}
	out := make([]agentContextPromptDTO, 0, len(stored))
	for _, prompt := range stored {
		out = append(out, agentContextPromptView(prompt))
	}
	return out, true
}

// putContextMCPConfig stores the custom MCP servers of one scope
// (contextcap.PutScopeMCPConfig), or writes the error response and returns
// false.
func (h *Handler) putContextMCPConfig(w http.ResponseWriter, r *http.Request, write contextcap.ScopeMCPConfigWrite) (contextcap.ScopeMCPConfig, bool) {
	ctx := r.Context()
	stored, err := contextcap.PutScopeMCPConfig(ctx, h.DB, write)
	switch {
	case errors.Is(err, contextcap.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid mcp_config")
		return stored, false
	case errors.Is(err, contextcap.ErrNotFound):
		writeError(w, http.StatusNotFound, "scope not found")
		return stored, false
	case err != nil:
		slog.ErrorContext(ctx, "context builder: MCP config write failed", "agent_id", write.AgentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save the custom MCP servers")
		return stored, false
	}
	return stored, true
}

// PutContextConfigPrompts replaces the prompt components of an org, scene or
// person scope from the configure page:
// PUT /api/context-capabilities/agents/{agentId}/prompts
// {scope_type, scope_key, org_id?, prompts: [{name, order, text, enabled?}]}
// → {prompts: [{id, name, order, text, enabled, updated_by_name, updated_at}]}.
// The admin PUT's whole-list replace and validation; a 1:1 chat scene acts on
// its person; 403 without contextCapRights.EditPrompts.
func (h *Handler) PutContextConfigPrompts(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return
	}
	a, ok := h.contextCapAgentOr404(w, r)
	if !ok {
		return
	}
	var input struct {
		ScopeType string                `json:"scope_type"`
		ScopeKey  string                `json:"scope_key"`
		OrgID     string                `json:"org_id"`
		Prompts   *[]contextPromptInput `json:"prompts"`
	}
	if !decodeContextCapBody(w, r, agentContextPromptsBodyLim, &input) {
		return
	}
	if a, ok = h.contextCapRequestOrg(w, r, a, userID, contextCapScopeOrg(input.ScopeType, input.ScopeKey, input.OrgID)); !ok {
		return
	}
	if input.Prompts == nil {
		writeError(w, http.StatusBadRequest, "prompts is required")
		return
	}
	components, ok := contextPromptComponents(w, *input.Prompts)
	if !ok {
		return
	}
	scope, ok := h.contextCapRequireScope(w, r, a, userID, input.ScopeType, input.ScopeKey, contextCapNeedPrompts)
	if !ok {
		return
	}
	out, ok := h.replaceContextPrompts(w, r, contextcap.PromptComponentsWrite{
		WorkspaceID: a.WorkspaceID, AgentID: a.ID, ScopeType: scope.ScopeType, OrgID: a.OrgID,
		ScopeKey: scope.ScopeKey, Components: components, ActorID: userID,
	})
	if !ok {
		return
	}
	slog.InfoContext(r.Context(), "context capabilities: prompts updated", "agent_id", a.ID, "workspace_id", a.WorkspaceID,
		"scope_type", scope.ScopeType, "prompt_count", len(out), "user_id", userID)
	writeJSON(w, http.StatusOK, map[string]any{"prompts": out})
}

// PutContextConfigMCPConfig replaces the custom MCP servers of an org, scene
// or person scope from the configure page:
// PUT /api/context-capabilities/agents/{agentId}/mcp-config
// {scope_type, scope_key, org_id?, mcp_config: {mcpServers: {...}} | null}
// → {mcp_config}. Remote servers only (contextcap.NormalizeRemoteMCPConfig:
// an http(s) url, optional type, headers and disabled; never command, args,
// env or cwd) and no reserved name (multica, c<16 hex>), else 400
// invalid_mcp_config; no server clears the scope. A 1:1 chat scene acts on
// its person; 403 without contextCapRights.EditMCP.
func (h *Handler) PutContextConfigMCPConfig(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return
	}
	a, ok := h.contextCapAgentOr404(w, r)
	if !ok {
		return
	}
	var input struct {
		ScopeType string          `json:"scope_type"`
		ScopeKey  string          `json:"scope_key"`
		OrgID     string          `json:"org_id"`
		MCPConfig json.RawMessage `json:"mcp_config"`
	}
	if !decodeContextCapBody(w, r, agentSceneMCPConfigBodyLimit, &input) {
		return
	}
	if a, ok = h.contextCapRequestOrg(w, r, a, userID, contextCapScopeOrg(input.ScopeType, input.ScopeKey, input.OrgID)); !ok {
		return
	}
	if len(input.MCPConfig) == 0 {
		writeError(w, http.StatusBadRequest, "mcp_config is required")
		return
	}
	config, ok := remoteMCPConfigOrBadRequest(w, input.MCPConfig)
	if !ok {
		return
	}
	scope, ok := h.contextCapRequireScope(w, r, a, userID, input.ScopeType, input.ScopeKey, contextCapNeedMCP)
	if !ok {
		return
	}
	stored, ok := h.putContextMCPConfig(w, r, contextcap.ScopeMCPConfigWrite{
		WorkspaceID: a.WorkspaceID, AgentID: a.ID, ScopeType: scope.ScopeType, OrgID: a.OrgID,
		ScopeKey: scope.ScopeKey, MCPConfig: config, ActorID: userID,
	})
	if !ok {
		return
	}
	slog.InfoContext(r.Context(), "context capabilities: custom MCP servers updated", "agent_id", a.ID, "workspace_id", a.WorkspaceID,
		"scope_type", scope.ScopeType, "cleared", stored.MCPConfig == nil, "user_id", userID)
	writeJSON(w, http.StatusOK, map[string]any{"mcp_config": stored.MCPConfig})
}

// remoteMCPConfigOrBadRequest validates a configure-page custom MCP server
// configuration (contextcap.NormalizeRemoteMCPConfig, plus no server named
// like a managed server of the claim, reservedMCPServerName) and returns it
// normalized (a JSON null when it names no server), or writes 400
// invalid_mcp_config.
func remoteMCPConfigOrBadRequest(w http.ResponseWriter, raw json.RawMessage) (json.RawMessage, bool) {
	config, err := contextcap.NormalizeRemoteMCPConfig(raw)
	if err != nil {
		reason := "mcp_config must be {\"mcpServers\": {...}} of remote servers"
		var invalid *contextcap.InvalidMCPConfigError
		if errors.As(err, &invalid) {
			reason = invalid.Reason
		}
		writeErrorCode(w, http.StatusBadRequest, contextCapErrInvalidMCPConfig, reason)
		return nil, false
	}
	if config == nil {
		return json.RawMessage("null"), true
	}
	servers, err := contextcap.MCPServers(config)
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, contextCapErrInvalidMCPConfig, "mcpServers must be an object of servers")
		return nil, false
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if reservedMCPServerName(name) {
			writeErrorCode(w, http.StatusBadRequest, contextCapErrInvalidMCPConfig,
				"server name "+name+" is reserved for a server Multica manages; choose another name")
			return nil, false
		}
	}
	return config, true
}
