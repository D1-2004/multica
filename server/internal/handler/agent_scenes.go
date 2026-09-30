package handler

import (
	"context"
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
// (场域提示词) and scene bindings. Workspace-scoped agent routes, human actor
// only, same permission as editing the agent's skills (workspace owner/admin
// or the agent owner); only that set may write the scene prompt.
//
// Configuration only: the scene prompt and 1:1 scene bindings are stored and
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

type agentSceneOfferedConnectorDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	CatalogSlug string `json:"catalog_slug"`
	AuthMode    string `json:"auth_mode"`
}

type agentSceneDetailResponse struct {
	Scene    agentSceneDTO          `json:"scene"`
	Prompt   agentScenePromptDTO    `json:"prompt"`
	Bindings []agentSceneBindingDTO `json:"bindings"`
	Offers   struct {
		Connectors []agentSceneOfferedConnectorDTO `json:"connectors"`
		Skills     []contextCapSkillDTO            `json:"skills"`
	} `json:"offers"`
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

// GetAgentScene returns one scene with its prompt, scene bindings (offered
// resources only) and the offer catalog to toggle from:
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
	resp, err := h.buildAgentSceneDetail(r.Context(), caller, scene)
	if err != nil {
		slog.ErrorContext(r.Context(), "agent scenes: detail failed", "agent_id", caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load scene")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) buildAgentSceneDetail(ctx context.Context, caller agentSceneCaller, scene contextcap.SceneSummary) (agentSceneDetailResponse, error) {
	var resp agentSceneDetailResponse
	resp.Scene = agentSceneView(scene)
	resp.Bindings = []agentSceneBindingDTO{}
	resp.Offers.Connectors = []agentSceneOfferedConnectorDTO{}
	resp.Offers.Skills = []contextCapSkillDTO{}

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
	bindings, err := contextcap.ListScopeBindings(ctx, h.DB, caller.workspaceID, caller.agentID, contextcap.ScopeScene, caller.orgID, scene.SceneKey)
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

	if len(offers.ConnectorIDs) > 0 {
		rows, err := h.DB.Query(ctx, `SELECT id::text, name, catalog_slug, auth_mode FROM internal_connector
			WHERE workspace_id = $1::uuid AND id = ANY($2::uuid[]) AND enabled
			ORDER BY name, id`, caller.workspaceID, offers.ConnectorIDs)
		if err != nil {
			return resp, err
		}
		for rows.Next() {
			var c agentSceneOfferedConnectorDTO
			if err := rows.Scan(&c.ID, &c.Name, &c.CatalogSlug, &c.AuthMode); err != nil {
				rows.Close()
				return resp, err
			}
			resp.Offers.Connectors = append(resp.Offers.Connectors, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return resp, err
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
// {"resource_type","resource_id","enabled"}. Offer-gated like the mobile
// PUT: 403 unless the resource is in the enabled offer catalog.
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
	binding, err := contextcap.UpsertBinding(ctx, h.DB, contextcap.BindingWrite{
		WorkspaceID: caller.workspaceID, AgentID: caller.agentID, ScopeType: contextcap.ScopeScene, OrgID: caller.orgID,
		ScopeKey: scene.SceneKey, ScopeTitle: agentSceneTitle(scene), ResourceType: input.ResourceType, ResourceID: resourceID,
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
	slog.InfoContext(ctx, "agent scenes: binding updated", "agent_id", caller.agentID, "scene_kind", scene.Kind,
		"resource_type", binding.ResourceType, "resource_id", binding.ResourceID, "enabled", binding.Enabled, "actor_id", requestUserID(r))
	writeJSON(w, http.StatusOK, map[string]any{"binding": agentSceneBindingView(binding, names)})
}
