package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/contextcap"
)

// Admin tenant and Context Builder API (docs/context-capabilities.md §1.3,
// §6 "Tenants" and "Context nodes"). An agent reused by several enterprises
// has one tenant per enterprise (its DingTalk org); each tenant has an
// enterprise level (org), group scenes and people, and every level has a
// Context Builder of prompt components, MCP components (offered connectors
// and custom MCP servers) and skill components. Workspace-scoped agent
// routes, human actor only, the agent-manage permission (workspace
// owner/admin or the agent owner); agents get 403.

const (
	agentTenantBodyLimit       = 4 << 10
	agentContextPromptsBodyLim = 1 << 20
)

// Error codes of the tenant routes.
const (
	agentTenantErrExists      = "tenant_exists"
	agentTenantErrIdentity    = "identity_tenant"
	agentTenantErrInvalidOrg  = "invalid_org_id"
	agentTenantErrInvalidName = "invalid_name"
	agentContextErrPrompts    = "invalid_prompts"
	agentContextErrDuplicate  = "duplicate_prompt_name"
)

type agentTenantDTO struct {
	OrgID string `json:"org_id"`
	Name  string `json:"name"`
	// Source is "identity" for the agent's DingTalk identity org and
	// "created" for a tenant someone created.
	Source      string `json:"source"`
	GroupCount  int    `json:"group_count"`
	PersonCount int    `json:"person_count"`
}

type agentUnassignedOrgDTO struct {
	OrgID       string `json:"org_id"`
	GroupCount  int    `json:"group_count"`
	PersonCount int    `json:"person_count"`
}

func agentTenantView(t contextcap.Tenant, activity contextcap.OrgActivity) agentTenantDTO {
	return agentTenantDTO{OrgID: t.OrgID, Name: t.Name, Source: t.Source, GroupCount: activity.GroupCount, PersonCount: activity.PersonCount}
}

// agentTenantActivity is the org activity of the caller's agent (group and
// person counts per org).
func (h *Handler) agentTenantActivity(ctx context.Context, caller agentSceneCaller) (map[string]contextcap.OrgActivity, error) {
	return contextcap.ListAgentOrgActivity(ctx, h.DB, caller.workspaceID, caller.agentID, caller.orgID)
}

// ListAgentTenants lists the agent's tenants and the orgs its scene and
// person data mentions without a tenant: GET /api/agents/{id}/tenants →
// {tenants: [T], unassigned_orgs: [{org_id, group_count, person_count}]}.
func (h *Handler) ListAgentTenants(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.agentSceneAdmin(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tenants, err := contextcap.AgentTenants(ctx, h.DB, caller.workspaceID, caller.agentID)
	if err != nil {
		slog.ErrorContext(ctx, "agent tenants: list failed", "agent_id", caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list tenants")
		return
	}
	activity, err := h.agentTenantActivity(ctx, caller)
	if err != nil {
		slog.ErrorContext(ctx, "agent tenants: activity failed", "agent_id", caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list tenants")
		return
	}
	out := make([]agentTenantDTO, 0, len(tenants))
	isTenant := map[string]bool{}
	for _, tenant := range tenants {
		isTenant[tenant.OrgID] = true
		out = append(out, agentTenantView(tenant, activity[tenant.OrgID]))
	}
	unassigned := []agentUnassignedOrgDTO{}
	for org, counts := range activity {
		if !isTenant[org] {
			unassigned = append(unassigned, agentUnassignedOrgDTO{OrgID: org, GroupCount: counts.GroupCount, PersonCount: counts.PersonCount})
		}
	}
	sort.Slice(unassigned, func(i, j int) bool { return unassigned[i].OrgID < unassigned[j].OrgID })
	writeJSON(w, http.StatusOK, map[string]any{"tenants": out, "unassigned_orgs": unassigned})
}

// CreateAgentTenant creates a tenant: POST /api/agents/{id}/tenants
// {org_id, name} → 201 {tenant: T}. 400 invalid_org_id / invalid_name,
// 409 tenant_exists (a tenant row, or the agent's identity org).
func (h *Handler) CreateAgentTenant(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.agentSceneAdmin(w, r)
	if !ok {
		return
	}
	var input struct {
		OrgID string `json:"org_id"`
		Name  string `json:"name"`
	}
	if !decodeContextCapBody(w, r, agentTenantBodyLimit, &input) {
		return
	}
	orgID := strings.TrimSpace(input.OrgID)
	if !contextcap.ValidTenantOrgID(orgID) {
		writeErrorCode(w, http.StatusBadRequest, agentTenantErrInvalidOrg, "org_id must be 1-64 letters, digits, '_' or '-'")
		return
	}
	if _, ok := contextcap.NormalizeTenantName(input.Name); !ok {
		writeErrorCode(w, http.StatusBadRequest, agentTenantErrInvalidName, "name must be 1-64 characters")
		return
	}
	ctx := r.Context()
	tenant, err := contextcap.CreateTenant(ctx, h.DB, contextcap.TenantWrite{
		WorkspaceID: caller.workspaceID, AgentID: caller.agentID, OrgID: orgID, Name: input.Name, ActorID: requestUserID(r),
	})
	switch {
	case errors.Is(err, contextcap.ErrTenantExists):
		writeErrorCode(w, http.StatusConflict, agentTenantErrExists, "this organization is already a tenant of the agent")
		return
	case errors.Is(err, contextcap.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid tenant")
		return
	case err != nil:
		slog.ErrorContext(ctx, "agent tenants: create failed", "agent_id", caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create the tenant")
		return
	}
	activity, err := h.agentTenantActivity(ctx, caller)
	if err != nil {
		activity = map[string]contextcap.OrgActivity{}
	}
	slog.InfoContext(ctx, "agent tenants: created", "agent_id", caller.agentID, "workspace_id", caller.workspaceID, "org_id", orgID,
		"actor_id", requestUserID(r))
	writeJSON(w, http.StatusCreated, map[string]any{"tenant": agentTenantView(tenant, activity[tenant.OrgID])})
}

// agentTenantFromRoute resolves the {orgId} route param to a tenant of the
// caller's agent: 404 tenant_not_found otherwise.
func (h *Handler) agentTenantFromRoute(w http.ResponseWriter, r *http.Request, caller agentSceneCaller) (contextcap.Tenant, bool) {
	orgID, ok := contextCapPathParam(r, "orgId")
	if !ok || !contextcap.ValidOrgID(orgID) {
		writeErrorCode(w, http.StatusNotFound, contextCapErrTenantNotFound, "tenant not found")
		return contextcap.Tenant{}, false
	}
	tenant, err := contextcap.AgentTenant(r.Context(), h.DB, caller.workspaceID, caller.agentID, orgID)
	if errors.Is(err, contextcap.ErrNotFound) {
		writeErrorCode(w, http.StatusNotFound, contextCapErrTenantNotFound, "tenant not found")
		return contextcap.Tenant{}, false
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "agent tenants: lookup failed", "agent_id", caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load the tenant")
		return contextcap.Tenant{}, false
	}
	return tenant, true
}

// RenameAgentTenant renames a tenant: PATCH /api/agents/{id}/tenants/{orgId}
// {name} → {tenant: T}. The identity tenant is renamed by storing a row.
func (h *Handler) RenameAgentTenant(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.agentSceneAdmin(w, r)
	if !ok {
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if !decodeContextCapBody(w, r, agentTenantBodyLimit, &input) {
		return
	}
	if _, ok := contextcap.NormalizeTenantName(input.Name); !ok {
		writeErrorCode(w, http.StatusBadRequest, agentTenantErrInvalidName, "name must be 1-64 characters")
		return
	}
	current, ok := h.agentTenantFromRoute(w, r, caller)
	if !ok {
		return
	}
	ctx := r.Context()
	tenant, err := contextcap.RenameTenant(ctx, h.DB, contextcap.TenantWrite{
		WorkspaceID: caller.workspaceID, AgentID: caller.agentID, OrgID: current.OrgID, Name: input.Name, ActorID: requestUserID(r),
	})
	switch {
	case errors.Is(err, contextcap.ErrNotFound):
		writeErrorCode(w, http.StatusNotFound, contextCapErrTenantNotFound, "tenant not found")
		return
	case errors.Is(err, contextcap.ErrInvalidInput):
		writeErrorCode(w, http.StatusBadRequest, agentTenantErrInvalidOrg, "this organization id cannot be stored as a tenant")
		return
	case err != nil:
		slog.ErrorContext(ctx, "agent tenants: rename failed", "agent_id", caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to rename the tenant")
		return
	}
	activity, err := h.agentTenantActivity(ctx, caller)
	if err != nil {
		activity = map[string]contextcap.OrgActivity{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenant": agentTenantView(tenant, activity[tenant.OrgID])})
}

// DeleteAgentTenant removes a created tenant and its enterprise-level
// configuration in one transaction: DELETE /api/agents/{id}/tenants/{orgId}
// → 204. Its group and person data stays (the org shows as unassigned).
// 409 identity_tenant for the agent's identity org.
func (h *Handler) DeleteAgentTenant(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.agentSceneAdmin(w, r)
	if !ok {
		return
	}
	tenant, ok := h.agentTenantFromRoute(w, r, caller)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete the tenant")
		return
	}
	defer tx.Rollback(ctx)
	err = contextcap.DeleteTenant(ctx, tx, caller.workspaceID, caller.agentID, tenant.OrgID)
	switch {
	case errors.Is(err, contextcap.ErrIdentityTenant):
		writeErrorCode(w, http.StatusConflict, agentTenantErrIdentity, "the agent's own DingTalk organization cannot be deleted")
		return
	case errors.Is(err, contextcap.ErrNotFound):
		writeErrorCode(w, http.StatusNotFound, contextCapErrTenantNotFound, "tenant not found")
		return
	case err != nil:
		slog.ErrorContext(ctx, "agent tenants: delete failed", "agent_id", caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to delete the tenant")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete the tenant")
		return
	}
	slog.InfoContext(ctx, "agent tenants: deleted", "agent_id", caller.agentID, "workspace_id", caller.workspaceID, "org_id", tenant.OrgID,
		"actor_id", requestUserID(r))
	w.WriteHeader(http.StatusNoContent)
}

// agentScenesPage parses ?limit=&offset= of a scene listing (limit 1..200,
// default 50; offset ≥ 0), writing 400 otherwise.
func agentScenesPage(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	limit, offset := agentScenesDefaultLimit, 0
	query := r.URL.Query()
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > agentScenesMaxLimit {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return 0, 0, false
		}
		limit = parsed
	}
	if raw := query.Get("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > agentScenesMaxOffset {
			writeError(w, http.StatusBadRequest, "invalid offset")
			return 0, 0, false
		}
		offset = parsed
	}
	return limit, offset, true
}

// ListAgentTenantGroups lists the group scenes of one tenant, newest
// activity first: GET /api/agents/{id}/tenants/{orgId}/groups?limit=&offset=
// → {scenes: [S], has_more}.
func (h *Handler) ListAgentTenantGroups(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.agentSceneAdmin(w, r)
	if !ok {
		return
	}
	limit, offset, ok := agentScenesPage(w, r)
	if !ok {
		return
	}
	tenant, ok := h.agentTenantFromRoute(w, r, caller)
	if !ok {
		return
	}
	scenes, hasMore, err := contextcap.ListAgentScenes(r.Context(), h.DB, contextcap.SceneListQuery{
		WorkspaceID: caller.workspaceID, AgentID: caller.agentID, OrgID: tenant.OrgID, IdentityOrgID: caller.orgID,
		Limit: limit, Offset: offset, GroupsOnly: true,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "agent tenants: group list failed", "agent_id", caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list groups")
		return
	}
	out := make([]agentSceneDTO, 0, len(scenes))
	for _, scene := range scenes {
		out = append(out, agentSceneView(scene))
	}
	writeJSON(w, http.StatusOK, map[string]any{"scenes": out, "has_more": hasMore})
}

type agentTenantPersonDTO struct {
	StaffID string `json:"staff_id"`
	Title   string `json:"title"`
	// DMSceneKey is the person's 1:1 chat with the agent ("" when unknown).
	DMSceneKey   string `json:"dm_scene_key"`
	LastActiveAt string `json:"last_active_at"`
}

// ListAgentTenantPersons lists the people known for one tenant, newest
// activity first: GET /api/agents/{id}/tenants/{orgId}/persons →
// {persons: [{staff_id, title, dm_scene_key, last_active_at}]}.
func (h *Handler) ListAgentTenantPersons(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.agentSceneAdmin(w, r)
	if !ok {
		return
	}
	tenant, ok := h.agentTenantFromRoute(w, r, caller)
	if !ok {
		return
	}
	persons, err := contextcap.ListOrgPersons(r.Context(), h.DB, caller.workspaceID, caller.agentID, tenant.OrgID, caller.orgID)
	if err != nil {
		slog.ErrorContext(r.Context(), "agent tenants: person list failed", "agent_id", caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list people")
		return
	}
	out := make([]agentTenantPersonDTO, 0, len(persons))
	for _, p := range persons {
		out = append(out, agentTenantPersonDTO{StaffID: p.StaffID, Title: p.Title, DMSceneKey: p.DMSceneKey, LastActiveAt: contextCapTime(p.LastActiveAt)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"persons": out})
}

// agentContextNode is one node of the 场域 tree resolved for a Context
// Builder call: the tenant, the agent working in the tenant's org, the
// effective scope (a 1:1 chat's is its person's) and the scene shown with
// it (the group, or the person's 1:1 chat).
type agentContextNode struct {
	caller agentSceneCaller
	tenant contextcap.Tenant
	agent  contextCapAgent
	scope  contextCapScope
	scene  *contextcap.SceneSummary
}

// agentContextNodeFromRoute resolves /tenants/{orgId}/context/{scopeType}/{scopeKey}
// for need: org (scopeKey = orgId), scene (a group or 1:1 chat the agent
// has seen in that org; a 1:1 chat maps to its person) or person (a person
// known for that org). 400 for a malformed scope, 404 for an unknown
// tenant, scene or person, 409 dm_person_unknown for a write to a 1:1 chat
// whose person is unknown, 403 person_only for a credential write on
// someone else's person scope.
func (h *Handler) agentContextNodeFromRoute(w http.ResponseWriter, r *http.Request, need contextCapNeed) (agentContextNode, bool) {
	caller, ok := h.agentSceneAdmin(w, r)
	if !ok {
		return agentContextNode{}, false
	}
	tenant, ok := h.agentTenantFromRoute(w, r, caller)
	if !ok {
		return agentContextNode{}, false
	}
	node := agentContextNode{caller: caller, tenant: tenant, agent: caller.contextCapAgent()}
	node.agent.OrgID, node.agent.OrgName = tenant.OrgID, tenant.Name
	ctx := r.Context()
	scopeType := chi.URLParam(r, "scopeType")
	scopeKey, ok := contextCapPathParam(r, "scopeKey")
	if !ok || !contextcap.ValidScopeKey(scopeType, scopeKey) {
		writeError(w, http.StatusBadRequest, "invalid scope")
		return agentContextNode{}, false
	}
	manages := true
	opts := contextCapResolveOptions{Manages: &manages, ManagerPersons: true}
	personTitle := ""
	switch scopeType {
	case contextcap.ScopeOrg:
		if scopeKey != tenant.OrgID {
			writeError(w, http.StatusBadRequest, "an org scope's key is its org id")
			return agentContextNode{}, false
		}
	case contextcap.ScopeScene:
		scene, err := contextcap.GetScene(ctx, h.DB, caller.workspaceID, caller.agentID, tenant.OrgID, caller.orgID, scopeKey)
		if errors.Is(err, contextcap.ErrNotFound) {
			writeError(w, http.StatusNotFound, "scene not found")
			return agentContextNode{}, false
		}
		if err != nil {
			slog.ErrorContext(ctx, "agent context: scene lookup failed", "agent_id", caller.agentID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to load scene")
			return agentContextNode{}, false
		}
		node.scene, opts.Scene = &scene, &scene
	case contextcap.ScopePerson:
		persons, err := contextcap.ListOrgPersons(ctx, h.DB, caller.workspaceID, caller.agentID, tenant.OrgID, caller.orgID)
		if err != nil {
			slog.ErrorContext(ctx, "agent context: person lookup failed", "agent_id", caller.agentID, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to load person")
			return agentContextNode{}, false
		}
		person, found := contextcap.FindPerson(persons, scopeKey)
		if !found {
			writeError(w, http.StatusNotFound, "person not found")
			return agentContextNode{}, false
		}
		personTitle = person.Title
		if person.DMSceneKey != "" {
			scene, err := contextcap.GetScene(ctx, h.DB, caller.workspaceID, caller.agentID, tenant.OrgID, caller.orgID, person.DMSceneKey)
			switch {
			case err == nil:
				node.scene = &scene
			case !errors.Is(err, contextcap.ErrNotFound):
				slog.ErrorContext(ctx, "agent context: 1:1 chat lookup failed", "agent_id", caller.agentID, "error", err)
				writeError(w, http.StatusInternalServerError, "failed to load person")
				return agentContextNode{}, false
			}
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid scope")
		return agentContextNode{}, false
	}
	scope, ok := h.contextCapRequireScopeWith(w, r, node.agent, requestUserID(r), scopeType, scopeKey, need, opts)
	if !ok {
		return agentContextNode{}, false
	}
	switch scope.ScopeType {
	case contextcap.ScopeOrg:
		scope.ScopeTitle = tenant.Name
	case contextcap.ScopeScene:
		scope.ScopeTitle = firstNonEmpty(agentSceneTitle(*node.scene), scope.ScopeTitle)
	case contextcap.ScopePerson:
		scope.ScopeTitle = firstNonEmpty(personTitle, scope.ScopeTitle)
	}
	node.scope = scope
	return node, true
}

type agentContextScopeDTO struct {
	Type  string `json:"type"`
	OrgID string `json:"org_id"`
	Key   string `json:"key"`
	Title string `json:"title"`
}

type agentContextPromptDTO struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Order         int    `json:"order"`
	Text          string `json:"text"`
	UpdatedByName string `json:"updated_by_name"`
	UpdatedAt     string `json:"updated_at"`
}

// agentContextConnectorDTO is one offered or granted connector on a node:
// the configure page's offer flags, this scope's credential and whether this
// scope switches it on.
type agentContextConnectorDTO struct {
	agentSceneOfferedConnectorDTO
	// Global: the agent grants it (通用能力), so it is on at every node and
	// the node may only give it its own account.
	Global  bool `json:"global"`
	Enabled bool `json:"enabled"`
}

type agentContextSkillDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

type agentContextEffectivePromptDTO struct {
	Name         string `json:"name"`
	Text         string `json:"text"`
	Layer        string `json:"layer"`
	OverriddenBy string `json:"overridden_by,omitempty"`
}

type agentContextEffectiveResourceDTO struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Layer string `json:"layer"`
}

type agentContextEffectiveServerDTO struct {
	Name         string `json:"name"`
	Layer        string `json:"layer"`
	OverriddenBy string `json:"overridden_by,omitempty"`
}

type agentContextEffectiveDTO struct {
	Prompts    []agentContextEffectivePromptDTO   `json:"prompts"`
	Connectors []agentContextEffectiveResourceDTO `json:"connectors"`
	Skills     []agentContextEffectiveResourceDTO `json:"skills"`
	MCPServers []agentContextEffectiveServerDTO   `json:"mcp_servers"`
}

type agentContextNodeResponse struct {
	// Scope is where this node's configuration lives: the org, the group
	// scene, or the person (a 1:1 chat's person); null for a 1:1 chat
	// whose person is unknown (no configuration then).
	Scope *agentContextScopeDTO `json:"scope"`
	// Scene is the group scene of a scene node, or the person's 1:1 chat
	// (null when there is none, and for org nodes).
	Scene      *agentSceneDTO             `json:"scene"`
	Prompts    []agentContextPromptDTO    `json:"prompts"`
	Connectors []agentContextConnectorDTO `json:"connectors"`
	Skills     []agentContextSkillDTO     `json:"skills"`
	// MCPConfig is the scope's custom MCP servers (null when none).
	MCPConfig         json.RawMessage `json:"mcp_config"`
	MCPConfigRedacted bool            `json:"mcp_config_redacted"`
	// CanConnect: the caller may store, remove or connect credentials of
	// Scope (managers for org and group scopes; only the person for a
	// person scope).
	CanConnect bool                     `json:"can_connect"`
	Effective  agentContextEffectiveDTO `json:"effective"`
}

// GetAgentContextNode returns one node's Context Builder:
// GET /api/agents/{id}/tenants/{orgId}/context/{scopeType}/{scopeKey}.
// effective is what a run of that node gets: global + org for an org node,
// + scene for a scene node, + person for a person node (mergeTaskContext, the
// merge the claim uses).
func (h *Handler) GetAgentContextNode(w http.ResponseWriter, r *http.Request) {
	node, ok := h.agentContextNodeFromRoute(w, r, contextCapNeedRead)
	if !ok {
		return
	}
	resp, err := h.buildAgentContextNode(r.Context(), node)
	if err != nil {
		slog.ErrorContext(r.Context(), "agent context: node failed", "agent_id", node.caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load the context")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) buildAgentContextNode(ctx context.Context, node agentContextNode) (agentContextNodeResponse, error) {
	caller, scope := node.caller, node.scope
	org := node.tenant.OrgID
	resp := agentContextNodeResponse{
		Prompts: []agentContextPromptDTO{}, Connectors: []agentContextConnectorDTO{}, Skills: []agentContextSkillDTO{},
		CanConnect: scope.CanConnect && !scope.PersonUnknown,
		Effective: agentContextEffectiveDTO{
			Prompts: []agentContextEffectivePromptDTO{}, Connectors: []agentContextEffectiveResourceDTO{},
			Skills: []agentContextEffectiveResourceDTO{}, MCPServers: []agentContextEffectiveServerDTO{},
		},
	}
	if !scope.PersonUnknown {
		resp.Scope = &agentContextScopeDTO{Type: scope.ScopeType, OrgID: org, Key: scope.ScopeKey, Title: scope.ScopeTitle}
	}
	if node.scene != nil {
		view := agentSceneView(*node.scene)
		resp.Scene = &view
	}

	offers, err := contextcap.ListOffers(ctx, h.DB, caller.workspaceID, caller.agentID)
	if err != nil {
		return resp, err
	}
	// The layers of the node: org, then the node's own scope.
	selection := contextcap.LayerSelection{OrgID: org, Org: true}
	if !scope.PersonUnknown {
		switch scope.ScopeType {
		case contextcap.ScopeScene:
			selection.SceneKey = scope.ScopeKey
		case contextcap.ScopePerson:
			selection.PersonKey = scope.ScopeKey
		}
	}
	layers, err := contextcap.LoadLayers(ctx, h.DB, caller.workspaceID, caller.agentID, selection)
	if err != nil {
		return resp, err
	}
	global, err := contextcap.LoadGlobalLayer(ctx, h.DB, caller.workspaceID, caller.agentID)
	if err != nil {
		return resp, err
	}

	enabled := map[string]bool{}
	hints := map[string]string{}
	if !scope.PersonUnknown {
		bindings, err := contextcap.ListScopeBindings(ctx, h.DB, caller.workspaceID, caller.agentID, scope.ScopeType, org, scope.ScopeKey)
		if err != nil {
			return resp, err
		}
		for _, binding := range bindings {
			if binding.Enabled && offers.Contains(binding.ResourceType, binding.ResourceID) {
				enabled[binding.ResourceType+":"+binding.ResourceID] = true
			}
		}
		credentials, err := contextcap.ListScopeCredentials(ctx, h.DB, caller.workspaceID, caller.agentID, scope.ScopeType, org, scope.ScopeKey)
		if err != nil {
			return resp, err
		}
		for _, credential := range credentials {
			hints[credential.ConnectorID] = credential.Hint
		}
		// The node's own layer is the last loaded one.
		own := layers[len(layers)-1]
		for _, prompt := range own.Prompts {
			resp.Prompts = append(resp.Prompts, agentContextPromptDTO{
				ID: prompt.ID, Name: prompt.Name, Order: prompt.Order, Text: prompt.Text,
				UpdatedByName: prompt.UpdatedByName, UpdatedAt: contextCapTime(prompt.UpdatedAt),
			})
		}
		if len(own.MCPConfig) > 0 {
			redact, err := h.agentSceneRedactsMCPConfig(ctx, caller.workspaceID)
			if err != nil {
				return resp, err
			}
			if redact {
				resp.MCPConfigRedacted = true
			} else {
				resp.MCPConfig = own.MCPConfig
			}
		}
	}
	// A person's account hint reaches workspace admins and that person
	// only; an org's or a group's reaches every manager.
	showAccount := scope.ScopeType != contextcap.ScopePerson || caller.workspaceAdmin || scope.CanConnect

	if scopeIDs := scopeConnectorIDs(offers.ConnectorIDs, global.ConnectorIDs); len(scopeIDs) > 0 {
		granted := make(map[string]bool, len(global.ConnectorIDs))
		for _, id := range global.ConnectorIDs {
			granted[id] = true
		}
		listed, err := h.queryInternalConnectors(ctx, `SELECT `+internalConnectorSelect+`
			FROM internal_connector c
			WHERE c.workspace_id = $1::uuid AND c.id = ANY($2::uuid[]) AND c.enabled
			ORDER BY c.name, c.id`, caller.workspaceID, scopeIDs)
		if err != nil {
			return resp, err
		}
		for _, c := range listed {
			if !offers.Contains(contextcap.ResourceConnector, c.ID) && !takesScopeAccount(c) {
				continue
			}
			accepts := connectorAcceptsBearer(c.AuthMode, c.CatalogSlug)
			item := agentContextConnectorDTO{
				agentSceneOfferedConnectorDTO: agentSceneOfferedConnectorDTO{
					ID: c.ID, Name: c.Name, CatalogSlug: c.CatalogSlug, AuthMode: c.AuthMode,
					AcceptsCredential: accepts, AcceptsPAT: c.AuthMode == "oauth" && accepts,
				},
				Global:  granted[c.ID],
				Enabled: enabled[contextcap.ResourceConnector+":"+c.ID],
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
			resp.Connectors = append(resp.Connectors, item)
		}
	}
	if len(offers.SkillIDs) > 0 {
		skills, err := h.contextCapSkills(ctx, `SELECT s.id::text, s.name, s.description
			FROM skill s WHERE s.workspace_id = $1::uuid AND s.id = ANY($2::uuid[])
			ORDER BY s.name, s.id`, caller.workspaceID, offers.SkillIDs)
		if err != nil {
			return resp, err
		}
		for _, skill := range skills {
			resp.Skills = append(resp.Skills, agentContextSkillDTO{
				ID: skill.ID, Name: skill.Name, Description: skill.Description, Enabled: enabled[contextcap.ResourceSkill+":"+skill.ID],
			})
		}
	}

	// The runtime's merge (mergeTaskContext), so the preview shows what a
	// run of the node gets.
	effective, _ := mergeTaskContext(append([]contextcap.ContextLayer{global}, layers...)...)
	if err := h.agentContextEffectiveView(ctx, caller.workspaceID, effective, &resp.Effective); err != nil {
		return resp, err
	}
	return resp, nil
}

// agentContextEffectiveView renders an effective context with resource
// names. Connectors that are gone or switched off in the workspace library
// and skills that no longer exist are left out, as the runtime leaves them
// out.
func (h *Handler) agentContextEffectiveView(ctx context.Context, workspaceID string, effective contextcap.EffectiveContext, out *agentContextEffectiveDTO) error {
	for _, prompt := range effective.Prompts {
		out.Prompts = append(out.Prompts, agentContextEffectivePromptDTO{Name: prompt.Name, Text: prompt.Text, Layer: prompt.Layer, OverriddenBy: prompt.OverriddenBy})
	}
	for _, server := range effective.MCPServers {
		out.MCPServers = append(out.MCPServers, agentContextEffectiveServerDTO{Name: server.Name, Layer: server.Layer, OverriddenBy: server.OverriddenBy})
	}
	names := func(query string, resources []contextcap.EffectiveResource) (map[string]string, error) {
		ids := make([]string, 0, len(resources))
		for _, resource := range resources {
			ids = append(ids, resource.ID)
		}
		found := map[string]string{}
		if len(ids) == 0 {
			return found, nil
		}
		rows, err := h.DB.Query(ctx, query, workspaceID, ids)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				return nil, err
			}
			found[id] = name
		}
		return found, rows.Err()
	}
	connectorNames, err := names(`SELECT id::text, name FROM internal_connector WHERE workspace_id = $1::uuid AND id = ANY($2::uuid[]) AND enabled`, effective.Connectors)
	if err != nil {
		return err
	}
	for _, connector := range effective.Connectors {
		if name, ok := connectorNames[connector.ID]; ok {
			out.Connectors = append(out.Connectors, agentContextEffectiveResourceDTO{ID: connector.ID, Name: name, Layer: connector.Layer})
		}
	}
	skillNames, err := names(`SELECT id::text, name FROM skill WHERE workspace_id = $1::uuid AND id = ANY($2::uuid[])`, effective.Skills)
	if err != nil {
		return err
	}
	for _, skill := range effective.Skills {
		if name, ok := skillNames[skill.ID]; ok {
			out.Skills = append(out.Skills, agentContextEffectiveResourceDTO{ID: skill.ID, Name: name, Layer: skill.Layer})
		}
	}
	return nil
}

// bindingTitle is the title snapshot a binding of the node stores.
func (node agentContextNode) bindingTitle() string {
	return node.scope.ScopeTitle
}

// PutAgentContextBinding turns one offered connector or skill on or off for
// a node: PUT .../context/{scopeType}/{scopeKey}/bindings
// {resource_type, resource_id, enabled} → {binding}. Offer-gated (403 unless
// offered, enable and disable alike).
func (h *Handler) PutAgentContextBinding(w http.ResponseWriter, r *http.Request) {
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
	node, ok := h.agentContextNodeFromRoute(w, r, contextCapNeedWrite)
	if !ok {
		return
	}
	ctx := r.Context()
	caller := node.caller
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
		WorkspaceID: caller.workspaceID, AgentID: caller.agentID, ScopeType: node.scope.ScopeType, OrgID: node.tenant.OrgID,
		ScopeKey: node.scope.ScopeKey, ScopeTitle: node.bindingTitle(), ResourceType: input.ResourceType, ResourceID: resourceID,
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
		slog.ErrorContext(ctx, "agent context: binding write failed", "agent_id", caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "binding write failed")
		return
	}
	names, err := h.agentSceneUserNames(ctx, []string{binding.UpdatedBy})
	if err != nil {
		names = map[string]string{}
	}
	slog.InfoContext(ctx, "agent context: binding updated", "agent_id", caller.agentID, "scope_type", node.scope.ScopeType,
		"resource_type", binding.ResourceType, "resource_id", binding.ResourceID, "enabled", binding.Enabled, "actor_id", requestUserID(r))
	writeJSON(w, http.StatusOK, map[string]any{"binding": agentSceneBindingView(binding, names)})
}

// PutAgentContextPrompts replaces a node's prompt components:
// PUT .../context/{scopeType}/{scopeKey}/prompts {prompts: [{name, order,
// text}]} → {prompts: [P]}. At most 20; names 1..64 characters and unique
// (400 duplicate_prompt_name); texts 1..8000 characters (400
// invalid_prompts). An empty list clears them.
func (h *Handler) PutAgentContextPrompts(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Prompts *[]struct {
			Name  string `json:"name"`
			Order int    `json:"order"`
			Text  string `json:"text"`
		} `json:"prompts"`
	}
	if !decodeContextCapBody(w, r, agentContextPromptsBodyLim, &input) {
		return
	}
	if input.Prompts == nil {
		writeError(w, http.StatusBadRequest, "prompts is required")
		return
	}
	components := make([]contextcap.PromptComponentInput, 0, len(*input.Prompts))
	for _, prompt := range *input.Prompts {
		components = append(components, contextcap.PromptComponentInput{Name: prompt.Name, Order: prompt.Order, Text: prompt.Text})
	}
	if _, err := contextcap.NormalizePromptComponents(components); err != nil {
		if errors.Is(err, contextcap.ErrDuplicatePromptName) {
			writeErrorCode(w, http.StatusBadRequest, agentContextErrDuplicate, "two prompts have the same name")
			return
		}
		writeErrorCode(w, http.StatusBadRequest, agentContextErrPrompts,
			"at most 20 prompts, each with a name of 1-64 characters and a text of 1-8000 characters")
		return
	}
	node, ok := h.agentContextNodeFromRoute(w, r, contextCapNeedWrite)
	if !ok {
		return
	}
	ctx := r.Context()
	caller := node.caller
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save the prompts")
		return
	}
	defer tx.Rollback(ctx)
	stored, err := contextcap.ReplacePromptComponents(ctx, tx, contextcap.PromptComponentsWrite{
		WorkspaceID: caller.workspaceID, AgentID: caller.agentID, ScopeType: node.scope.ScopeType, OrgID: node.tenant.OrgID,
		ScopeKey: node.scope.ScopeKey, Components: components, ActorID: requestUserID(r),
	})
	switch {
	case errors.Is(err, contextcap.ErrInvalidInput), errors.Is(err, contextcap.ErrNotFound):
		writeErrorCode(w, http.StatusBadRequest, agentContextErrPrompts, "invalid prompts")
		return
	case err != nil:
		slog.ErrorContext(ctx, "agent context: prompt write failed", "agent_id", caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save the prompts")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save the prompts")
		return
	}
	out := make([]agentContextPromptDTO, 0, len(stored))
	for _, prompt := range stored {
		out = append(out, agentContextPromptDTO{ID: prompt.ID, Name: prompt.Name, Order: prompt.Order, Text: prompt.Text,
			UpdatedByName: prompt.UpdatedByName, UpdatedAt: contextCapTime(prompt.UpdatedAt)})
	}
	slog.InfoContext(ctx, "agent context: prompts updated", "agent_id", caller.agentID, "workspace_id", caller.workspaceID,
		"scope_type", node.scope.ScopeType, "prompt_count", len(out), "actor_id", requestUserID(r))
	writeJSON(w, http.StatusOK, map[string]any{"prompts": out})
}

// PutAgentContextMCPConfig stores a node's custom MCP servers:
// PUT .../context/{scopeType}/{scopeKey}/mcp-config {mcp_config: object|null}
// → {mcp_config}. The agent mcp_config format, a JSON object of at most 64
// KiB; null or {} clears it.
func (h *Handler) PutAgentContextMCPConfig(w http.ResponseWriter, r *http.Request) {
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
	node, ok := h.agentContextNodeFromRoute(w, r, contextCapNeedWrite)
	if !ok {
		return
	}
	ctx := r.Context()
	caller := node.caller
	stored, err := contextcap.PutScopeMCPConfig(ctx, h.DB, contextcap.ScopeMCPConfigWrite{
		WorkspaceID: caller.workspaceID, AgentID: caller.agentID, ScopeType: node.scope.ScopeType, OrgID: node.tenant.OrgID,
		ScopeKey: node.scope.ScopeKey, MCPConfig: input.MCPConfig, ActorID: requestUserID(r),
	})
	switch {
	case errors.Is(err, contextcap.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid mcp_config")
		return
	case errors.Is(err, contextcap.ErrNotFound):
		writeError(w, http.StatusNotFound, "scope not found")
		return
	case err != nil:
		slog.ErrorContext(ctx, "agent context: MCP config write failed", "agent_id", caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save the custom MCP servers")
		return
	}
	slog.InfoContext(ctx, "agent context: custom MCP servers updated", "agent_id", caller.agentID, "workspace_id", caller.workspaceID,
		"scope_type", node.scope.ScopeType, "cleared", stored.MCPConfig == nil, "actor_id", requestUserID(r))
	writeJSON(w, http.StatusOK, map[string]any{"mcp_config": stored.MCPConfig})
}

// PutAgentContextCredential stores a pasted token for one connector in a
// node's scope: PUT .../context/{scopeType}/{scopeKey}/credentials
// {connector_id, bearer} → {credential: C}. Managers for org and group
// scopes; a person scope only by that person (403 person_only).
func (h *Handler) PutAgentContextCredential(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ConnectorID string `json:"connector_id"`
		Bearer      string `json:"bearer"`
	}
	if !decodeContextCapBody(w, r, contextCapBodyLimit, &input) {
		return
	}
	connectorUUID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(input.ConnectorID), "connector_id")
	if !ok {
		return
	}
	if !contextcap.ValidBearer(input.Bearer) {
		writeError(w, http.StatusBadRequest, "invalid Bearer credential")
		return
	}
	node, ok := h.agentContextNodeFromRoute(w, r, contextCapNeedCredential)
	if !ok {
		return
	}
	connectorID := uuidToString(connectorUUID)
	catalogSlug, ok := h.contextCapCredentialConnector(w, r, node.agent, node.scope.ScopeType, connectorID)
	if !ok {
		return
	}
	box := h.contextCredentialBox()
	if box == nil {
		writeError(w, http.StatusServiceUnavailable, "connector credential storage is not configured")
		return
	}
	ctx := r.Context()
	key := contextcap.CredentialBinding{
		WorkspaceID: node.caller.workspaceID, AgentID: node.caller.agentID, ConnectorID: connectorID,
		ScopeType: node.scope.ScopeType, OrgID: node.tenant.OrgID, ScopeKey: node.scope.ScopeKey,
	}
	sealed, err := contextcap.SealCredential(box, key, input.Bearer)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "credential could not be saved")
		return
	}
	stored, err := contextcap.UpsertCredential(ctx, h.DB, key, sealed, contextcap.Hint(input.Bearer), requestUserID(r))
	if err != nil {
		slog.ErrorContext(ctx, "agent context: credential write failed", "agent_id", node.caller.agentID, "connector_id", connectorID, "error", err)
		writeError(w, http.StatusInternalServerError, "credential could not be saved")
		return
	}
	slog.InfoContext(ctx, "agent context: credential stored", "agent_id", node.caller.agentID, "connector_id", connectorID,
		"scope_type", node.scope.ScopeType, "actor_id", requestUserID(r))
	if catalogSlug != "" {
		if _, _, err := h.discoverCatalogToolsIfEmpty(ctx, node.caller.workspaceID, connectorID, contextcap.Secret{Bearer: input.Bearer}, node.scope.ScopeType, key); err != nil {
			slog.WarnContext(ctx, "official app first tool discovery failed", "agent_id", node.caller.agentID, "connector_id", connectorID, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"credential": contextCapCredentialView(stored)})
}

// DeleteAgentContextCredential removes one connector credential of a
// node's scope: DELETE .../context/{scopeType}/{scopeKey}/credentials?connector_id=
// → 204 (idempotent). Same authority as the PUT.
func (h *Handler) DeleteAgentContextCredential(w http.ResponseWriter, r *http.Request) {
	connectorUUID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(r.URL.Query().Get("connector_id")), "connector_id")
	if !ok {
		return
	}
	node, ok := h.agentContextNodeFromRoute(w, r, contextCapNeedCredential)
	if !ok {
		return
	}
	connectorID := uuidToString(connectorUUID)
	if _, err := contextcap.DeleteCredential(r.Context(), h.DB, contextcap.CredentialBinding{
		WorkspaceID: node.caller.workspaceID, AgentID: node.caller.agentID, ConnectorID: connectorID,
		ScopeType: node.scope.ScopeType, OrgID: node.tenant.OrgID, ScopeKey: node.scope.ScopeKey,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "credential could not be removed")
		return
	}
	slog.InfoContext(r.Context(), "agent context: credential removed", "agent_id", node.caller.agentID, "connector_id", connectorID,
		"scope_type", node.scope.ScopeType, "actor_id", requestUserID(r))
	w.WriteHeader(http.StatusNoContent)
}

// RevokeAgentContextGrants removes every configuration grant on a group or
// person node: DELETE .../context/{scopeType}/{scopeKey}/grants →
// {revoked: n}. Configure-page access from links and the group picker ends
// at once (a 1:1 chat node revokes its person's grants, including the 1:1
// chat grants their personal links gave); the scope's configuration stays.
// A person whose personal scope a forwarded link handed to someone else can
// then redeem a new personal link. 400 for the org level, which has no
// grants. Managers only, like the other node writes.
func (h *Handler) RevokeAgentContextGrants(w http.ResponseWriter, r *http.Request) {
	node, ok := h.agentContextNodeFromRoute(w, r, contextCapNeedWrite)
	if !ok {
		return
	}
	if node.scope.ScopeType != contextcap.ScopeScene && node.scope.ScopeType != contextcap.ScopePerson {
		writeError(w, http.StatusBadRequest, "only group and person levels have configuration grants")
		return
	}
	ctx := r.Context()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke access")
		return
	}
	defer tx.Rollback(ctx)
	revoked, err := contextcap.RevokeGrants(ctx, tx, node.caller.workspaceID, node.caller.agentID, node.scope.ScopeType, node.tenant.OrgID, node.scope.ScopeKey)
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		slog.ErrorContext(ctx, "agent context: grant revoke failed", "agent_id", node.caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to revoke access")
		return
	}
	slog.InfoContext(ctx, "agent context: grants revoked", "agent_id", node.caller.agentID, "workspace_id", node.caller.workspaceID,
		"org_id", node.tenant.OrgID, "scope_type", node.scope.ScopeType, "revoked", revoked, "actor_id", requestUserID(r))
	writeJSON(w, http.StatusOK, map[string]any{"revoked": revoked})
}

// StartAgentContextConnection starts connecting an official app account for
// a node's scope through OAuth: POST .../context/{scopeType}/{scopeKey}/connections/start
// {connector_id, return_to?} → {authorize_url} plus the browser binding
// cookie. The state stores the tenant org; the callback re-checks the same
// authority.
func (h *Handler) StartAgentContextConnection(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ConnectorID string `json:"connector_id"`
		ReturnTo    string `json:"return_to"`
	}
	if !decodeContextCapBody(w, r, contextCapBodyLimit, &input) {
		return
	}
	connectorUUID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(input.ConnectorID), "connector_id")
	if !ok {
		return
	}
	node, ok := h.agentContextNodeFromRoute(w, r, contextCapNeedCredential)
	if !ok {
		return
	}
	started, err := h.startConnectorOAuth(r.Context(), connectorOAuthStart{
		connectorOAuthScope: connectorOAuthScope{
			WorkspaceID: node.caller.workspaceID, ConnectorID: uuidToString(connectorUUID), UserID: requestUserID(r),
			ScopeType: node.scope.ScopeType, AgentID: node.caller.agentID, OrgID: node.tenant.OrgID, ScopeKey: node.scope.ScopeKey,
			SceneKey: node.scope.DirectSceneKey,
		},
		ReturnTo: input.ReturnTo,
	})
	if err != nil {
		writeConnectorOAuthStartError(w, r, err, true)
		return
	}
	writeConnectorOAuthStarted(w, started)
}
