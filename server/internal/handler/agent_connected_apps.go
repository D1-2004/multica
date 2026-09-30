package handler

import (
	"context"
	"log/slog"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/connectorcatalog"
	"github.com/multica-ai/multica/server/internal/contextcap"
)

// Connected apps of an agent (docs/context-capabilities.md §6 "Connected
// apps"): one status row per official app in the catalog for the agent's
// 连接器 tab, block 「连接应用」. Workspace-scoped agent routes, human actor,
// the same permission as the other agent context capability routes
// (workspace owner/admin or the agent owner). Read-only: the page changes
// state through the existing catalog, connector, offer, credential and OAuth
// endpoints.
//
// Status rules: "connected" means a stored credential exists for the scope
// (for the shared account: a usable workspace credential); "enabled" means an
// enabled scene or person binding while the app is offered, because a binding
// of an app that is not offered is stored but never applies.
//
// The workspace connector library is admin-only (as in buildContextCapAdmin),
// so for an agent owner who is not a workspace admin an app that is neither
// granted to nor offered on this agent shows its catalog facts only, and the
// shared account never carries its hint.

// connectedAppToolsDTO counts the app's tools: discovered (the snapshot of
// the last discovery), allowed (pinned for runs) and read_only (discovered
// tools the app marks read-only).
type connectedAppToolsDTO struct {
	Discovered int `json:"discovered"`
	Allowed    int `json:"allowed"`
	ReadOnly   int `json:"read_only"`
}

// connectedAppSharedAccountDTO is the workspace shared account ("所有人共用").
type connectedAppSharedAccountDTO struct {
	// Connected: the workspace credential is usable (a pasted token, or an
	// OAuth token that is unexpired or refreshable).
	Connected bool `json:"connected"`
	// Account is its display hint: "@login" or "OAuth" for an OAuth account,
	// "••••abcd" for a pasted token; "" when not connected, for an
	// environment credential and for callers who are not workspace admins.
	Account string `json:"account"`
	// Source is "workspace" for the stored workspace credential (the page
	// can disconnect it), "environment" for the operator-managed
	// MULTICA_INTERNAL_MCP_BEARER_<id> fallback (DELETE .../credential cannot
	// remove it), "" when not connected.
	Source string `json:"source"`
}

type connectedAppUsageDTO struct {
	ScenesEnabled    int `json:"scenes_enabled"`
	ScenesConnected  int `json:"scenes_connected"`
	PersonsEnabled   int `json:"persons_enabled"`
	PersonsConnected int `json:"persons_connected"`
}

// connectedAppDTO is one entry of GET /api/agents/{id}/connected-apps.
type connectedAppDTO struct {
	catalogAppFacts
	// InstallURL is the GitHub App installation page ("" when none).
	InstallURL string `json:"install_url"`
	// ConnectorID is the workspace catalog connector (null until the app is
	// added to the workspace).
	ConnectorID *string `json:"connector_id"`
	// Added: the app is on this agent (granted or offered).
	Added              bool                         `json:"added"`
	EnabledInWorkspace bool                         `json:"enabled_in_workspace"`
	GlobalEnabled      bool                         `json:"global_enabled"`
	Offered            bool                         `json:"offered"`
	WriteEnabled       bool                         `json:"write_enabled"`
	Tools              connectedAppToolsDTO         `json:"tools"`
	SharedAccount      connectedAppSharedAccountDTO `json:"shared_account"`
	Usage              connectedAppUsageDTO         `json:"usage"`
}

type connectedAppSceneDTO struct {
	SceneKey string `json:"scene_key"`
	Title    string `json:"title"`
	// Kind is "group" or "dm".
	Kind      string `json:"kind"`
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"`
	Account   string `json:"account"`
}

type connectedAppPersonDTO struct {
	ScopeKey      string `json:"scope_key"`
	Title         string `json:"title"`
	Enabled       bool   `json:"enabled"`
	Connected     bool   `json:"connected"`
	Account       string `json:"account"`
	ShareInGroups bool   `json:"share_in_groups"`
}

type connectedAppToolDTO struct {
	Name     string `json:"name"`
	ReadOnly bool   `json:"read_only"`
	Allowed  bool   `json:"allowed"`
}

// connectedAppDetailDTO is GET /api/agents/{id}/connected-apps/{slug}: the
// list entry plus who uses the app and its tools.
type connectedAppDetailDTO struct {
	connectedAppDTO
	Scenes   []connectedAppSceneDTO  `json:"scenes"`
	Persons  []connectedAppPersonDTO `json:"persons"`
	ToolList []connectedAppToolDTO   `json:"tool_list"`
	CanAdmin bool                    `json:"can_admin"`
}

// connectedAppsState is what the connected-apps views are built from.
type connectedAppsState struct {
	caller     agentSceneCaller
	connectors map[string]internalConnector // by catalog slug
	granted    map[string]bool              // connector id → globally granted to the agent
	offers     contextcap.Offers
	uses       map[string][]contextcap.ConnectorScopeUse // connector id → scopes
}

// loadConnectedApps reads the workspace's catalog connectors, the agent's
// grants and offers, and the agent's scene and person uses under its current
// DingTalk org of the connector of app usesOf (of every catalog connector
// when usesOf is "").
func (h *Handler) loadConnectedApps(ctx context.Context, caller agentSceneCaller, usesOf string) (connectedAppsState, error) {
	state := connectedAppsState{
		caller: caller, connectors: map[string]internalConnector{}, granted: map[string]bool{},
		uses: map[string][]contextcap.ConnectorScopeUse{},
	}
	connectors, err := h.queryInternalConnectors(ctx, `SELECT `+internalConnectorSelect+`
		FROM internal_connector c WHERE c.workspace_id = $1::uuid AND c.catalog_slug <> ''
		ORDER BY c.created_at, c.id`, caller.workspaceID)
	if err != nil {
		return state, err
	}
	ids := make([]string, 0, len(connectors))
	for _, c := range connectors {
		if _, seen := state.connectors[c.CatalogSlug]; !seen {
			state.connectors[c.CatalogSlug] = c
			if usesOf == "" || c.CatalogSlug == usesOf {
				ids = append(ids, c.ID)
			}
		}
	}
	rows, err := h.DB.Query(ctx, `SELECT connector_id::text FROM internal_connector_agent
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid`, caller.workspaceID, caller.agentID)
	if err != nil {
		return state, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return state, err
		}
		state.granted[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return state, err
	}
	if state.offers, err = contextcap.ListOffers(ctx, h.DB, caller.workspaceID, caller.agentID); err != nil {
		return state, err
	}
	uses, err := contextcap.ListConnectorScopeUses(ctx, h.DB, caller.workspaceID, caller.agentID, caller.orgID, ids)
	if err != nil {
		return state, err
	}
	for _, use := range uses {
		state.uses[use.ConnectorID] = append(state.uses[use.ConnectorID], use)
	}
	return state, nil
}

// connectedAppView builds the status row of one official app.
func (h *Handler) connectedAppView(app connectorcatalog.App, state connectedAppsState) connectedAppDTO {
	view := connectedAppDTO{catalogAppFacts: h.catalogAppFactsView(app), InstallURL: catalogAppInstallURL(app)}
	c, ok := state.connectors[app.Slug]
	if !ok {
		return view
	}
	granted := state.granted[c.ID]
	offered := state.offers.Contains(contextcap.ResourceConnector, c.ID)
	if !state.caller.workspaceAdmin && !granted && !offered {
		// A library-only connector: admin-only, like the connector library.
		return view
	}
	id := c.ID
	view.ConnectorID = &id
	view.EnabledInWorkspace = c.Enabled
	view.GlobalEnabled = granted
	view.Offered = offered
	view.Added = view.GlobalEnabled || view.Offered
	view.WriteEnabled = c.WriteEnabled
	view.Tools = connectedAppTools(c)
	view.SharedAccount = h.connectedAppSharedAccount(c)
	if !state.caller.workspaceAdmin {
		// As with connectorCredentialAccount, no part of the workspace
		// credential reaches a caller outside the workspace admins.
		view.SharedAccount.Account = ""
	}
	for _, use := range state.uses[c.ID] {
		enabled := use.Enabled && view.Offered
		switch use.ScopeType {
		case contextcap.ScopeScene:
			if enabled {
				view.Usage.ScenesEnabled++
			}
			if use.Connected {
				view.Usage.ScenesConnected++
			}
		case contextcap.ScopePerson:
			if enabled {
				view.Usage.PersonsEnabled++
			}
			if use.Connected {
				view.Usage.PersonsConnected++
			}
		}
	}
	return view
}

func connectedAppTools(c internalConnector) connectedAppToolsDTO {
	tools := connectedAppToolsDTO{Discovered: len(c.DiscoveredTools), Allowed: len(c.AllowedTools)}
	for _, tool := range c.DiscoveredTools {
		if tool.ReadOnly {
			tools.ReadOnly++
		}
	}
	return tools
}

// connectedAppSharedAccount reports the workspace shared account of a
// catalog connector (connectorWorkspaceCredential): connected only while the
// credential the relay sends is usable. The stored workspace credential
// comes with its hint. The operator's environment fallback does serve runs,
// so it is connected too, but with source "environment" and no hint: it is a
// deployment secret the page can neither show nor disconnect. It never
// returns token material.
func (h *Handler) connectedAppSharedAccount(c internalConnector) connectedAppSharedAccountDTO {
	switch source, secret := h.connectorWorkspaceCredential(c); source {
	case "workspace":
		account := contextcap.Hint(secret.Bearer)
		if secret.OAuth != nil {
			account = contextcap.OAuthHint(secret.OAuth.Account)
		}
		return connectedAppSharedAccountDTO{Connected: true, Account: account, Source: source}
	case "environment":
		return connectedAppSharedAccountDTO{Connected: true, Source: source}
	default:
		return connectedAppSharedAccountDTO{}
	}
}

// ListAgentConnectedApps answers GET /api/agents/{id}/connected-apps: every
// official app of the catalog with its status for this agent, plus whether
// the caller may administer the workspace connector library (can_admin).
func (h *Handler) ListAgentConnectedApps(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.agentSceneAdmin(w, r)
	if !ok {
		return
	}
	state, err := h.loadConnectedApps(r.Context(), caller, "")
	if err != nil {
		slog.ErrorContext(r.Context(), "connected apps: list failed", "agent_id", caller.agentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load connected apps")
		return
	}
	apps := []connectedAppDTO{}
	for _, app := range connectorCatalog.Apps() {
		apps = append(apps, h.connectedAppView(app, state))
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": apps, "can_admin": caller.workspaceAdmin})
}

// GetAgentConnectedApp answers GET /api/agents/{id}/connected-apps/{slug}:
// the app's status row plus the scenes and people that turned it on or
// connected their own account, and its discovered tools. 404 for a slug that
// is not in the catalog.
func (h *Handler) GetAgentConnectedApp(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.agentSceneAdmin(w, r)
	if !ok {
		return
	}
	app, ok := catalogApp(chi.URLParam(r, "slug"))
	if !ok {
		writeError(w, http.StatusNotFound, "official app not found")
		return
	}
	ctx := r.Context()
	state, err := h.loadConnectedApps(ctx, caller, app.Slug)
	if err != nil {
		slog.ErrorContext(ctx, "connected apps: detail failed", "agent_id", caller.agentID, "catalog_slug", app.Slug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load the connected app")
		return
	}
	detail, err := h.buildConnectedAppDetail(ctx, app, state)
	if err != nil {
		slog.ErrorContext(ctx, "connected apps: detail failed", "agent_id", caller.agentID, "catalog_slug", app.Slug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load the connected app")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *Handler) buildConnectedAppDetail(ctx context.Context, app connectorcatalog.App, state connectedAppsState) (connectedAppDetailDTO, error) {
	detail := connectedAppDetailDTO{
		connectedAppDTO: h.connectedAppView(app, state),
		Scenes:          []connectedAppSceneDTO{}, Persons: []connectedAppPersonDTO{}, ToolList: []connectedAppToolDTO{},
		CanAdmin: state.caller.workspaceAdmin,
	}
	c, ok := state.connectors[app.Slug]
	if !ok || detail.ConnectorID == nil {
		// Not in the workspace, or a library-only connector the caller may
		// not see (connectedAppView).
		return detail, nil
	}
	allowed := make(map[string]bool, len(c.AllowedTools))
	for _, name := range c.AllowedTools {
		allowed[name] = true
	}
	for _, tool := range c.DiscoveredTools {
		detail.ToolList = append(detail.ToolList, connectedAppToolDTO{Name: tool.Name, ReadOnly: tool.ReadOnly, Allowed: allowed[tool.Name]})
	}

	caller := state.caller
	uses := []contextcap.ConnectorScopeUse{}
	sceneKeys := []string{}
	for _, use := range state.uses[c.ID] {
		if !(use.Enabled && detail.Offered) && !use.Connected {
			continue
		}
		uses = append(uses, use)
		if use.ScopeType == contextcap.ScopeScene && contextcap.ValidOpenConversationID(use.ScopeKey) {
			sceneKeys = append(sceneKeys, use.ScopeKey)
		}
	}
	if len(uses) == 0 {
		return detail, nil
	}
	titles, err := contextcap.GrantTitles(ctx, h.DB, caller.workspaceID, caller.agentID, caller.orgID)
	if err != nil {
		return detail, err
	}
	// The used scenes the agent has seen (the admin scene union: memory,
	// Coordinator conversations, configuration, bindings), newest activity
	// first, for their title, kind and order. A key lookup is not paged.
	var scenes []contextcap.SceneSummary
	if len(sceneKeys) > 0 {
		if scenes, _, err = contextcap.ListAgentScenes(ctx, h.DB, contextcap.SceneListQuery{
			WorkspaceID: caller.workspaceID, AgentID: caller.agentID, OrgID: caller.orgID, Keys: sceneKeys,
		}); err != nil {
			return detail, err
		}
	}
	known := make(map[string]contextcap.SceneSummary, len(scenes))
	for _, scene := range scenes {
		known[scene.SceneKey] = scene
	}
	entries := make(map[string]connectedAppSceneDTO, len(scenes))
	unknown := []connectedAppSceneDTO{}
	for _, use := range uses {
		enabled := use.Enabled && detail.Offered
		title := firstNonEmpty(use.ScopeTitle, titles[contextcap.ScopeRef{ScopeType: use.ScopeType, ScopeKey: use.ScopeKey}])
		switch use.ScopeType {
		case contextcap.ScopeScene:
			entry := connectedAppSceneDTO{SceneKey: use.ScopeKey, Title: title, Kind: contextcap.SceneKindGroup, Enabled: enabled, Connected: use.Connected, Account: use.Hint}
			scene, found := known[use.ScopeKey]
			if !found {
				unknown = append(unknown, entry)
				continue
			}
			entry.Title = firstNonEmpty(agentSceneTitle(scene), title)
			entry.Kind = scene.Kind
			entries[use.ScopeKey] = entry
		case contextcap.ScopePerson:
			// A person's account hint (their provider login, or the tail of
			// their token) is shown to workspace admins only, like the
			// shared account's; an agent owner sees 已连接 without it.
			account := ""
			if caller.workspaceAdmin {
				account = use.Hint
			}
			detail.Persons = append(detail.Persons, connectedAppPersonDTO{
				ScopeKey: use.ScopeKey, Title: title, Enabled: enabled, Connected: use.Connected, Account: account,
				ShareInGroups: use.ShareInGroups,
			})
		}
	}
	// Scenes in activity order (newest first), unknown ones last by key;
	// people by name.
	for _, scene := range scenes {
		detail.Scenes = append(detail.Scenes, entries[scene.SceneKey])
	}
	sort.Slice(unknown, func(i, j int) bool { return unknown[i].SceneKey < unknown[j].SceneKey })
	detail.Scenes = append(detail.Scenes, unknown...)
	sort.SliceStable(detail.Persons, func(i, j int) bool {
		if detail.Persons[i].Title != detail.Persons[j].Title {
			return detail.Persons[i].Title < detail.Persons[j].Title
		}
		return detail.Persons[i].ScopeKey < detail.Persons[j].ScopeKey
	})
	return detail, nil
}
