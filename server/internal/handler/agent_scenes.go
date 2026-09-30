package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/service/scenememory"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Admin scene API (docs/context-capabilities.md §6 "Scenes"): an agent's IM
// scenes (DingTalk group chats and 1:1 chats), each scene's prompt
// (场域提示词), bindings and custom MCP servers. Workspace-scoped agent
// routes, human actor only, same permission as editing the agent's skills
// (workspace owner/admin or the agent owner); only that set may write the
// scene prompt.
//
// A group scene's configuration is its own. A 1:1 chat scene's bindings,
// credentials and custom MCP servers are its counterpart person's
// (contextCapResolveScope); only its prompt belongs to the scene.
//
// Configuration only: the scene prompt and custom MCP servers are stored and
// shown here, not yet applied at runtime.

const (
	agentScenesDefaultLimit   = 50
	agentScenesMaxLimit       = 200
	agentScenesMaxOffset      = 1 << 20
	agentScenePromptBodyLimit = 64 << 10
)

type agentSceneDTO struct {
	SceneKey string `json:"scene_key"`
	// Kind is "group" or "dm".
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	OrgID        string `json:"org_id"`
	LastActiveAt string `json:"last_active_at"`
	// InboundSessionID is the newest Coordinator chat session of the
	// conversation, usable with /coordinator-conversations/{sessionId}/messages
	// ("" when there is none). InboundCount counts the sessions that
	// transcript shows (same endpoint namespace and source).
	InboundSessionID string `json:"inbound_session_id"`
	InboundCount     int    `json:"inbound_count"`
	// MemoryID is the scene_memory row id ("" when the scene has none).
	MemoryID  string `json:"memory_id"`
	HasPrompt bool   `json:"has_prompt"`
}

type agentScenePromptDTO struct {
	Text string `json:"text"`
	// UpdatedAt and UpdatedByName describe the last prompt write ("" when
	// nobody has written one).
	UpdatedAt     string `json:"updated_at"`
	UpdatedByName string `json:"updated_by_name"`
}

type agentSceneBindingDTO struct {
	ResourceType  string `json:"resource_type"`
	ResourceID    string `json:"resource_id"`
	Enabled       bool   `json:"enabled"`
	UpdatedByName string `json:"updated_by_name"`
	UpdatedAt     string `json:"updated_at"`
}

// agentSceneCredentialDTO is the credential of an offered connector in the
// scene detail's effective scope.
type agentSceneCredentialDTO struct {
	// Connected: a credential (an OAuth account or a pasted token) is
	// stored for the connector in the scope.
	Connected bool `json:"connected"`
	// Account is its hint ("@login", "OAuth" or "••••abcd"); "" when not
	// connected, and for a person's scope unless the caller is a workspace
	// admin or that person.
	Account string `json:"account"`
}

type agentSceneOfferedConnectorDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	CatalogSlug string `json:"catalog_slug"`
	AuthMode    string `json:"auth_mode"`
	// AcceptsCredential, AcceptsPAT, OAuthAvailable and InstallURL follow
	// the configure page's offer rules (contextCapOfferedConnectorDTO).
	AcceptsCredential bool                    `json:"accepts_credential"`
	AcceptsPAT        bool                    `json:"accepts_pat"`
	OAuthAvailable    bool                    `json:"oauth_available"`
	InstallURL        string                  `json:"install_url,omitempty"`
	Credential        agentSceneCredentialDTO `json:"credential"`
}

type agentSceneDetailResponse struct {
	Scene  agentSceneDTO       `json:"scene"`
	Prompt agentScenePromptDTO `json:"prompt"`
	// Scope is where this scene's configuration lives: the scene for a
	// group, the counterpart person for a 1:1 chat; null for a 1:1 chat
	// whose person is unknown (no configuration then).
	Scope *contextCapScopeRef `json:"scope"`
	// Bindings are those of Scope.
	Bindings []agentSceneBindingDTO `json:"bindings"`
	Offers   struct {
		Connectors []agentSceneOfferedConnectorDTO `json:"connectors"`
		Skills     []contextCapSkillDTO            `json:"skills"`
	} `json:"offers"`
	// MCPConfig is the custom MCP servers of Scope (null when none).
	MCPConfig json.RawMessage `json:"mcp_config"`
	// MCPConfigRedacted: the workspace always redacts secrets, so an
	// existing MCPConfig is withheld (null), as for the agent's mcp_config.
	MCPConfigRedacted bool `json:"mcp_config_redacted"`
	// CanConnect: the caller may store, remove or connect credentials of
	// Scope through the configure page's routes (never a manager for a
	// person; those routes also need a DingTalk sign-in).
	CanConnect bool `json:"can_connect"`
}

// agentSceneCaller is the route agent of an admin scene call, its workspace
// and agent ids, its current DingTalk org ("" without an identity) and
// whether the caller is a workspace owner/admin (not only the agent owner).
type agentSceneCaller struct {
	agent          db.Agent
	workspaceID    string
	agentID        string
	orgID          string
	workspaceAdmin bool
}

// agentSceneAdmin loads the route agent for an admin scene call: a human
// caller with the agent-manage permission (contextCapAdminAgent). Agents may
// not read or write scenes, as with scene memory.
func (h *Handler) agentSceneAdmin(w http.ResponseWriter, r *http.Request) (agentSceneCaller, bool) {
	caller, ok := h.contextCapAdminAgent(w, r)
	if !ok {
		return agentSceneCaller{}, false
	}
	agent := caller.agent
	out := agentSceneCaller{agent: agent, workspaceID: uuidToString(agent.WorkspaceID), agentID: uuidToString(agent.ID), workspaceAdmin: caller.workspaceAdmin}
	if actorType, _ := h.resolveActor(r, requestUserID(r), out.workspaceID); actorType == "agent" {
		writeError(w, http.StatusForbidden, "agents may not manage scenes")
		return agentSceneCaller{}, false
	}
	identity, err := h.Queries.GetAgentDingTalkIdentity(r.Context(), db.GetAgentDingTalkIdentityParams{WorkspaceID: agent.WorkspaceID, AgentID: agent.ID})
	switch {
	case err == nil:
		out.orgID = strings.TrimSpace(identity.OrgID)
	case errors.Is(err, pgx.ErrNoRows):
	default:
		slog.ErrorContext(r.Context(), "agent scenes: DingTalk identity lookup failed", "agent_id", out.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load agent scenes")
		return agentSceneCaller{}, false
	}
	return out, true
}

// contextCapAgent is the caller's agent in the shape the context capability
// resolver takes.
func (c agentSceneCaller) contextCapAgent() contextCapAgent {
	return contextCapAgent{Agent: c.agent, ID: c.agentID, WorkspaceID: c.workspaceID, OrgID: c.orgID}
}

// agentSceneScope resolves where a scene's configuration lives for an admin
// call (contextCapResolveScope with the loaded scene; the caller manages
// the agent) and writes the refusal for need: 409 dm_person_unknown for a
// write to a 1:1 chat whose person is unknown.
func (h *Handler) agentSceneScope(w http.ResponseWriter, r *http.Request, caller agentSceneCaller, scene contextcap.SceneSummary, need contextCapNeed) (contextCapScope, bool) {
	manages := true
	return h.contextCapRequireScopeWith(w, r, caller.contextCapAgent(), requestUserID(r), contextcap.ScopeScene, scene.SceneKey, need,
		contextCapResolveOptions{Scene: &scene, Manages: &manages})
}

// contextCapDingTalkSession reports whether the request comes from a human
// DingTalk sign-in, which the configure page's routes require
// (RequireDingTalkHumanActor).
func contextCapDingTalkSession(r *http.Request) bool {
	return r.Header.Get("X-Actor-Source") == "" && r.Header.Get("X-Auth-Method") == "dingtalk"
}

// agentSceneFromRoute resolves the {sceneKey} route param (percent-encoded
// openConversationId) to a scene the agent has seen: 400 for a malformed key,
// 404 for an unknown scene.
func (h *Handler) agentSceneFromRoute(w http.ResponseWriter, r *http.Request, caller agentSceneCaller) (contextcap.SceneSummary, bool) {
	sceneKey, ok := contextCapPathParam(r, "sceneKey")
	if !ok || !contextcap.ValidOpenConversationID(sceneKey) {
		writeError(w, http.StatusBadRequest, "invalid scene key")
		return contextcap.SceneSummary{}, false
	}
	scene, err := contextcap.GetScene(r.Context(), h.DB, caller.workspaceID, caller.agentID, caller.orgID, sceneKey)
	if errors.Is(err, contextcap.ErrNotFound) {
		writeError(w, http.StatusNotFound, "scene not found")
		return contextcap.SceneSummary{}, false
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "agent scenes: scene lookup failed", "agent_id", caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load scene")
		return contextcap.SceneSummary{}, false
	}
	return scene, true
}

// agentSceneTitle picks the display title of a scene: the scene memory title
// (or its locating line), the Coordinator conversation title, the 1:1
// sender, then the stored configuration and binding snapshots.
func agentSceneTitle(s contextcap.SceneSummary) string {
	title := firstNonEmpty(scenememory.DisplayTitle(s.MemoryTitle, s.MemoryText), s.ConversationTitle)
	if title == "" && s.Kind == contextcap.SceneKindDM {
		title = strings.TrimSpace(s.SenderName)
	}
	return firstNonEmpty(title, s.ConfigTitle, s.BindingTitle)
}

func agentSceneView(s contextcap.SceneSummary) agentSceneDTO {
	return agentSceneDTO{
		SceneKey: s.SceneKey, Kind: s.Kind, Title: agentSceneTitle(s), OrgID: s.OrgID,
		LastActiveAt: contextCapTime(s.LastActiveAt), InboundSessionID: s.InboundSessionID, InboundCount: s.InboundCount,
		MemoryID: s.MemoryID, HasPrompt: s.HasPrompt,
	}
}

func agentScenePromptView(c contextcap.SceneConfig) agentScenePromptDTO {
	view := agentScenePromptDTO{Text: c.Prompt}
	// A row registered by a 1:1 link redemption has no prompt write yet.
	if c.UpdatedBy != "" {
		view.UpdatedAt = contextCapTime(c.UpdatedAt)
		view.UpdatedByName = c.UpdatedByName
	}
	return view
}

// agentSceneUserNames maps user ids to display names.
func (h *Handler) agentSceneUserNames(ctx context.Context, ids []string) (map[string]string, error) {
	names := map[string]string{}
	seen := map[string]bool{}
	unique := make([]string, 0, len(ids))
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	if len(unique) == 0 {
		return names, nil
	}
	rows, err := h.DB.Query(ctx, `SELECT id::text, name FROM "user" WHERE id = ANY($1::uuid[])`, unique)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}

func agentSceneBindingView(b contextcap.Binding, names map[string]string) agentSceneBindingDTO {
	return agentSceneBindingDTO{
		ResourceType: b.ResourceType, ResourceID: b.ResourceID, Enabled: b.Enabled,
		UpdatedByName: names[b.UpdatedBy], UpdatedAt: contextCapTime(b.UpdatedAt),
	}
}

// ListAgentScenes lists the agent's IM scenes (group and 1:1 chats), newest
// activity first: GET /api/agents/{id}/scenes?limit=&offset=.
func (h *Handler) ListAgentScenes(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.agentSceneAdmin(w, r)
	if !ok {
		return
	}
	limit, offset := agentScenesDefaultLimit, 0
	query := r.URL.Query()
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > agentScenesMaxLimit {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = parsed
	}
	if raw := query.Get("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > agentScenesMaxOffset {
			writeError(w, http.StatusBadRequest, "invalid offset")
			return
		}
		offset = parsed
	}
	scenes, hasMore, err := contextcap.ListAgentScenes(r.Context(), h.DB, contextcap.SceneListQuery{
		WorkspaceID: caller.workspaceID, AgentID: caller.agentID, OrgID: caller.orgID, Limit: limit, Offset: offset,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "agent scenes: list failed", "agent_id", caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list scenes")
		return
	}
	out := make([]agentSceneDTO, 0, len(scenes))
	for _, scene := range scenes {
		out = append(out, agentSceneView(scene))
	}
	writeJSON(w, http.StatusOK, map[string]any{"scenes": out, "has_more": hasMore})
}

// GetAgentScene returns one scene with its prompt, where its configuration
// lives (scope), that scope's bindings (offered resources only), custom MCP
// servers and connector credentials, and the offer catalog to toggle from:
// GET /api/agents/{id}/scenes/{sceneKey}.
func (h *Handler) GetAgentScene(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.agentSceneAdmin(w, r)
	if !ok {
		return
	}
	scene, ok := h.agentSceneFromRoute(w, r, caller)
	if !ok {
		return
	}
	scope, ok := h.agentSceneScope(w, r, caller, scene, contextCapNeedRead)
	if !ok {
		return
	}
	resp, err := h.buildAgentSceneDetail(r.Context(), caller, scene, scope, contextCapDingTalkSession(r))
	if err != nil {
		slog.ErrorContext(r.Context(), "agent scenes: detail failed", "agent_id", caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load scene")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// agentSceneRedactsMCPConfig reports whether the workspace always redacts
// secrets, which withholds custom MCP servers like the agent's mcp_config.
func (h *Handler) agentSceneRedactsMCPConfig(ctx context.Context, workspaceID string) (bool, error) {
	ws, err := h.Queries.GetWorkspace(ctx, parseUUID(workspaceID))
	if err != nil {
		return false, err
	}
	return workspaceAlwaysRedactSecrets(ws.Settings), nil
}

func (h *Handler) buildAgentSceneDetail(ctx context.Context, caller agentSceneCaller, scene contextcap.SceneSummary, scope contextCapScope, dingTalkSession bool) (agentSceneDetailResponse, error) {
	var resp agentSceneDetailResponse
	resp.Scene = agentSceneView(scene)
	resp.Scope = scope.ref()
	resp.Bindings = []agentSceneBindingDTO{}
	resp.Offers.Connectors = []agentSceneOfferedConnectorDTO{}
	resp.Offers.Skills = []contextCapSkillDTO{}
	resp.CanConnect = scope.CanConnect && !scope.PersonUnknown && dingTalkSession

	config, err := contextcap.GetSceneConfig(ctx, h.DB, caller.workspaceID, caller.agentID, caller.orgID, scene.SceneKey)
	switch {
	case err == nil:
		resp.Prompt = agentScenePromptView(config)
	case !errors.Is(err, contextcap.ErrNotFound):
		return resp, err
	}

	offers, err := contextcap.ListOffers(ctx, h.DB, caller.workspaceID, caller.agentID)
	if err != nil {
		return resp, err
	}
	// The effective scope's configuration: nothing for a 1:1 chat whose
	// person is unknown.
	hints := map[string]string{}
	if !scope.PersonUnknown {
		bindings, err := contextcap.ListScopeBindings(ctx, h.DB, caller.workspaceID, caller.agentID, scope.ScopeType, caller.orgID, scope.ScopeKey)
		if err != nil {
			return resp, err
		}
		actors := make([]string, 0, len(bindings))
		for _, binding := range bindings {
			actors = append(actors, binding.UpdatedBy)
		}
		names, err := h.agentSceneUserNames(ctx, actors)
		if err != nil {
			return resp, err
		}
		for _, binding := range bindings {
			if offers.Contains(binding.ResourceType, binding.ResourceID) {
				resp.Bindings = append(resp.Bindings, agentSceneBindingView(binding, names))
			}
		}
		credentials, err := contextcap.ListScopeCredentials(ctx, h.DB, caller.workspaceID, caller.agentID, scope.ScopeType, caller.orgID, scope.ScopeKey)
		if err != nil {
			return resp, err
		}
		for _, credential := range credentials {
			hints[credential.ConnectorID] = credential.Hint
		}
		config, err := contextcap.GetScopeMCPConfig(ctx, h.DB, caller.workspaceID, caller.agentID, scope.ScopeType, caller.orgID, scope.ScopeKey)
		switch {
		case err == nil:
			redact, err := h.agentSceneRedactsMCPConfig(ctx, caller.workspaceID)
			if err != nil {
				return resp, err
			}
			if redact {
				resp.MCPConfigRedacted = true
			} else {
				resp.MCPConfig = config.MCPConfig
			}
		case !errors.Is(err, contextcap.ErrNotFound):
			return resp, err
		}
	}
	// A person's account hint (their provider login, or the tail of their
	// token) reaches workspace admins and that person only, as on the
	// connected-apps page; a scene's reaches every manager.
	showAccount := scope.ScopeType == contextcap.ScopeScene || caller.workspaceAdmin || scope.CanConnect

	if len(offers.ConnectorIDs) > 0 {
		offered, err := h.queryInternalConnectors(ctx, `SELECT `+internalConnectorSelect+`
			FROM internal_connector c
			WHERE c.workspace_id = $1::uuid AND c.id = ANY($2::uuid[]) AND c.enabled
			ORDER BY c.name, c.id`, caller.workspaceID, offers.ConnectorIDs)
		if err != nil {
			return resp, err
		}
		for _, c := range offered {
			accepts := connectorAcceptsBearer(c.AuthMode, c.CatalogSlug)
			item := agentSceneOfferedConnectorDTO{
				ID: c.ID, Name: c.Name, CatalogSlug: c.CatalogSlug, AuthMode: c.AuthMode,
				AcceptsCredential: accepts, AcceptsPAT: c.AuthMode == "oauth" && accepts,
			}
			if app, ok := catalogApp(c.CatalogSlug); ok {
				item.InstallURL = catalogAppInstallURL(app)
				item.OAuthAvailable = c.AuthMode == "oauth" && h.catalogOAuthAvailable(app)
			}
			if hint, connected := hints[c.ID]; connected {
				item.Credential.Connected = true
				if showAccount {
					item.Credential.Account = hint
				}
			}
			resp.Offers.Connectors = append(resp.Offers.Connectors, item)
		}
	}
	if len(offers.SkillIDs) > 0 {
		if resp.Offers.Skills, err = h.contextCapSkills(ctx, `SELECT s.id::text, s.name, s.description
			FROM skill s WHERE s.workspace_id = $1::uuid AND s.id = ANY($2::uuid[])
			ORDER BY s.name, s.id`, caller.workspaceID, offers.SkillIDs); err != nil {
			return resp, err
		}
	}
	return resp, nil
}

// PutAgentScenePrompt stores the scene prompt (场域提示词) of a scene the
// agent has seen: PUT /api/agents/{id}/scenes/{sceneKey}/prompt {"prompt"}.
// The prompt is trimmed and at most 8000 characters; "" clears it. Stored
// only this round; runs do not read it yet.
func (h *Handler) PutAgentScenePrompt(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.agentSceneAdmin(w, r)
	if !ok {
		return
	}
	var input struct {
		Prompt *string `json:"prompt"`
	}
	if !decodeContextCapBody(w, r, agentScenePromptBodyLimit, &input) {
		return
	}
	if input.Prompt == nil {
		writeError(w, http.StatusBadRequest, "prompt is required")
		return
	}
	prompt := strings.TrimSpace(*input.Prompt)
	if !contextcap.ValidScenePrompt(prompt) {
		writeError(w, http.StatusBadRequest, "prompt must be at most 8000 characters of text")
		return
	}
	scene, ok := h.agentSceneFromRoute(w, r, caller)
	if !ok {
		return
	}
	ctx := r.Context()
	stored, err := contextcap.UpsertScenePrompt(ctx, h.DB, contextcap.SceneConfigWrite{
		WorkspaceID: caller.workspaceID, AgentID: caller.agentID, OrgID: caller.orgID, SceneKey: scene.SceneKey,
		SceneKind: scene.Kind, SceneTitle: agentSceneTitle(scene), Prompt: prompt, ActorID: requestUserID(r),
	})
	switch {
	case errors.Is(err, contextcap.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid scene prompt")
		return
	case errors.Is(err, contextcap.ErrNotFound):
		writeError(w, http.StatusNotFound, "scene not found")
		return
	case err != nil:
		slog.ErrorContext(ctx, "agent scenes: prompt write failed", "agent_id", caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save the scene prompt")
		return
	}
	slog.InfoContext(ctx, "agent scenes: prompt updated", "agent_id", caller.agentID, "workspace_id", caller.workspaceID,
		"scene_kind", scene.Kind, "prompt_chars", len([]rune(prompt)), "actor_id", requestUserID(r))
	writeJSON(w, http.StatusOK, map[string]any{"prompt": agentScenePromptView(stored)})
}

// PutAgentSceneBinding turns one offered connector or skill on or off for a
// scene the agent has seen: PUT /api/agents/{id}/scenes/{sceneKey}/bindings
// {"resource_type","resource_id","enabled"}. It writes the scene's
// effective scope (a 1:1 chat's person; 409 dm_person_unknown when unknown).
// Offer-gated like the mobile PUT: 403 unless the resource is in the enabled
// offer catalog.
func (h *Handler) PutAgentSceneBinding(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.agentSceneAdmin(w, r)
	if !ok {
		return
	}
	var input struct {
		ResourceType string `json:"resource_type"`
		ResourceID   string `json:"resource_id"`
		Enabled      *bool  `json:"enabled"`
	}
	if !decodeContextCapBody(w, r, contextCapBodyLimit, &input) {
		return
	}
	if input.Enabled == nil || (input.ResourceType != contextcap.ResourceConnector && input.ResourceType != contextcap.ResourceSkill) {
		writeError(w, http.StatusBadRequest, "resource_type and enabled are required")
		return
	}
	resourceUUID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(input.ResourceID), "resource_id")
	if !ok {
		return
	}
	resourceID := uuidToString(resourceUUID)
	scene, ok := h.agentSceneFromRoute(w, r, caller)
	if !ok {
		return
	}
	scope, ok := h.agentSceneScope(w, r, caller, scene, contextCapNeedWrite)
	if !ok {
		return
	}
	ctx := r.Context()
	offers, err := contextcap.ListOffers(ctx, h.DB, caller.workspaceID, caller.agentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offer lookup failed")
		return
	}
	if !offers.Contains(input.ResourceType, resourceID) {
		writeError(w, http.StatusForbidden, "this item is not offered by the agent")
		return
	}
	title := scope.ScopeTitle
	if scope.ScopeType == contextcap.ScopeScene {
		title = firstNonEmpty(agentSceneTitle(scene), title)
	}
	binding, err := contextcap.UpsertBinding(ctx, h.DB, contextcap.BindingWrite{
		WorkspaceID: caller.workspaceID, AgentID: caller.agentID, ScopeType: scope.ScopeType, OrgID: caller.orgID,
		ScopeKey: scope.ScopeKey, ScopeTitle: title, ResourceType: input.ResourceType, ResourceID: resourceID,
		Enabled: *input.Enabled, ActorID: requestUserID(r),
	})
	switch {
	case errors.Is(err, contextcap.ErrNotOffered):
		writeError(w, http.StatusForbidden, "this item is not offered by the agent")
		return
	case errors.Is(err, contextcap.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid binding")
		return
	case err != nil:
		slog.ErrorContext(ctx, "agent scenes: binding write failed", "agent_id", caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "binding write failed")
		return
	}
	names, err := h.agentSceneUserNames(ctx, []string{binding.UpdatedBy})
	if err != nil {
		names = map[string]string{}
	}
	slog.InfoContext(ctx, "agent scenes: binding updated", "agent_id", caller.agentID, "scene_kind", scene.Kind, "scope_type", scope.ScopeType,
		"resource_type", binding.ResourceType, "resource_id", binding.ResourceID, "enabled", binding.Enabled, "actor_id", requestUserID(r))
	writeJSON(w, http.StatusOK, map[string]any{"binding": agentSceneBindingView(binding, names)})
}

// agentSceneMCPConfigBodyLimit bounds PUT .../mcp-config: the configuration
// plus its envelope.
const agentSceneMCPConfigBodyLimit = contextcap.MaxScopeMCPConfigBytes + 1<<10

// PutAgentSceneMCPConfig stores the custom MCP servers (「自定义 MCP 服务器」)
// of a scene the agent has seen: PUT /api/agents/{id}/scenes/{sceneKey}/mcp-config
// {"mcp_config": <object|null>} → {"mcp_config"}. The configuration is the
// agent mcp_config format (a JSON object, at most 64 KiB); null or {} clears
// it and deletes the row. A group scene stores it in the scene scope, a 1:1
// chat in its person's scope (409 dm_person_unknown when the person is
// unknown). Stored only this round; runs do not read it yet.
func (h *Handler) PutAgentSceneMCPConfig(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.agentSceneAdmin(w, r)
	if !ok {
		return
	}
	var input struct {
		MCPConfig json.RawMessage `json:"mcp_config"`
	}
	if !decodeContextCapBody(w, r, agentSceneMCPConfigBodyLimit, &input) {
		return
	}
	if len(input.MCPConfig) == 0 {
		writeError(w, http.StatusBadRequest, "mcp_config is required")
		return
	}
	if _, err := contextcap.NormalizeScopeMCPConfig(input.MCPConfig); err != nil {
		writeError(w, http.StatusBadRequest, "mcp_config must be a JSON object of at most 64 KiB, or null")
		return
	}
	scene, ok := h.agentSceneFromRoute(w, r, caller)
	if !ok {
		return
	}
	scope, ok := h.agentSceneScope(w, r, caller, scene, contextCapNeedWrite)
	if !ok {
		return
	}
	ctx := r.Context()
	stored, err := contextcap.PutScopeMCPConfig(ctx, h.DB, contextcap.ScopeMCPConfigWrite{
		WorkspaceID: caller.workspaceID, AgentID: caller.agentID, ScopeType: scope.ScopeType, OrgID: caller.orgID,
		ScopeKey: scope.ScopeKey, MCPConfig: input.MCPConfig, ActorID: requestUserID(r),
	})
	switch {
	case errors.Is(err, contextcap.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid mcp_config")
		return
	case errors.Is(err, contextcap.ErrNotFound):
		writeError(w, http.StatusNotFound, "scene not found")
		return
	case err != nil:
		slog.ErrorContext(ctx, "agent scenes: MCP config write failed", "agent_id", caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save the custom MCP servers")
		return
	}
	slog.InfoContext(ctx, "agent scenes: custom MCP servers updated", "agent_id", caller.agentID, "workspace_id", caller.workspaceID,
		"scene_kind", scene.Kind, "scope_type", scope.ScopeType, "cleared", stored.MCPConfig == nil, "actor_id", requestUserID(r))
	writeJSON(w, http.StatusOK, map[string]any{"mcp_config": stored.MCPConfig})
}
