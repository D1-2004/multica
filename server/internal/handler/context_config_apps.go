package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/connectorcatalog"
	"github.com/multica-ai/multica/server/internal/contextcap"
)

// Official apps on the configure page: a level adds any catalog app and
// completes its connection there, without the admin console.
//
//   - POST …/apps/{slug} adds the app at a level and needs only the level's
//     switch right (冬翔, 2026-10-02: a scene's members configure their
//     scene; the enterprise level stays with its managers). An app already
//     offered to (or owned by) the agent is only switched on; another
//     catalog app is installed in the workspace and offered to the agent's
//     scopes first. An app an admin switched off is refused (409).
//   - GET|PUT|DELETE …/apps/{slug}/oauth-app reads, saves and removes a
//     scene's own OAuth application of an app without dynamic registration
//     (Slack, Asana, GitHub), with the callback URL to register in the
//     provider's console. A connection started at that scene signs in with
//     it, and its tokens are exchanged and refreshed with it; without one
//     the scene uses the workspace's. 冬翔 (2026-10-02): a scene's members
//     save the first one, only the agent's managers change or remove it, and
//     the workspace's and the enterprise's OAuth applications stay in the
//     admin console.

const contextConfigAppBodyLimit = 64 << 10

// Refusal codes of the configure page's official app routes.
const (
	contextConfigErrAppDisabled = "app_disabled"
	// contextConfigErrOAuthAppSceneOnly: the page saves only a scene's own
	// OAuth application.
	contextConfigErrOAuthAppSceneOnly = "oauth_app_scene_only"
	// contextConfigErrOAuthAppLocked: a saved scene OAuth application is
	// changed by the agent's managers only.
	contextConfigErrOAuthAppLocked = "oauth_app_locked"
)

// contextCapOfferedCatalogConnector returns the id of the agent's own or
// offered connector of the catalog app slug, "" when there is none.
func (h *Handler) contextCapOfferedCatalogConnector(ctx context.Context, a contextCapAgent, slug string) (string, error) {
	var id string
	err := h.DB.QueryRow(ctx, `SELECT c.id::text FROM internal_connector c
		WHERE c.workspace_id = $1::uuid AND c.catalog_slug = $3
		  AND (EXISTS (SELECT 1 FROM internal_connector_agent g
		               WHERE g.connector_id = c.id AND g.workspace_id = c.workspace_id AND g.agent_id = $2::uuid)
		    OR EXISTS (SELECT 1 FROM context_capability_binding o
		               WHERE o.workspace_id = c.workspace_id AND o.agent_id = $2::uuid AND o.scope_type = 'offer'
		                 AND o.resource_type = 'connector' AND o.resource_id = c.id AND o.enabled))
		ORDER BY c.created_at ASC LIMIT 1`, a.WorkspaceID, a.ID, slug).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// contextConfigAppSetup is how an app's OAuth sign-in gets ready:
// "automatic" (dynamic registration, or a deployment client), "oauth_app"
// (the workspace saves an OAuth application first) or "unsupported" (the
// provider refuses dynamic registration and offers no client to register).
func contextConfigAppSetup(slug string) string {
	spec, ok := connectorcatalog.SettingsSpecFor(slug)
	if !ok {
		return "automatic"
	}
	switch spec.Mode {
	case "preregistered":
		// The deployment's own client (the GitHub App env) needs nothing
		// from the workspace.
		if connectorAppEnvConfigured(slug) {
			return "automatic"
		}
		return "oauth_app"
	case "limited":
		return "unsupported"
	default:
		return "automatic"
	}
}

// AddContextConfigApp adds an official app at a level:
// POST /api/context-capabilities/agents/{agentId}/apps/{slug}
// {scope_type, scope_key, org_id?} → {connector_id, default_on}.
func (h *Handler) AddContextConfigApp(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return
	}
	a, ok := h.contextCapAgentOr404(w, r)
	if !ok {
		return
	}
	var input struct {
		ScopeType string `json:"scope_type"`
		ScopeKey  string `json:"scope_key"`
		OrgID     string `json:"org_id"`
	}
	if !decodeContextCapBody(w, r, contextConfigAppBodyLimit, &input) {
		return
	}
	if a, ok = h.contextCapRequestOrg(w, r, a, userID, contextCapScopeOrg(input.ScopeType, input.ScopeKey, input.OrgID)); !ok {
		return
	}
	slug := strings.TrimSpace(chi.URLParam(r, "slug"))
	if _, known := catalogApp(slug); !known {
		writeError(w, http.StatusNotFound, "official app not found")
		return
	}
	scope, ok := h.contextCapRequireScope(w, r, a, userID, input.ScopeType, input.ScopeKey, contextCapNeedToggle)
	if !ok {
		return
	}
	ctx := r.Context()
	offered, err := h.contextCapOfferedCatalogConnector(ctx, a, slug)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "offer lookup failed")
		return
	}
	var connector internalConnector
	created := false
	if offered != "" {
		// Already the agent's own or offered: only this level switches it on.
		connector, err = h.loadInternalConnector(ctx, a.WorkspaceID, offered)
	} else {
		connector, created, err = h.createCatalogConnector(ctx, a.WorkspaceID, slug)
	}
	if err != nil {
		slog.ErrorContext(ctx, "context capabilities: official app install failed", "agent_id", a.ID, "catalog_slug", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to add the official app")
		return
	}
	if !connector.Enabled {
		writeErrorCode(w, http.StatusConflict, contextConfigErrAppDisabled, "a workspace admin switched this app off")
		return
	}
	granted, err := h.grantedConnectors(ctx, a.WorkspaceID, a.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "connector lookup failed")
		return
	}
	for _, c := range granted {
		if c.ID == connector.ID {
			// The agent's own connector is on at every level already.
			writeJSON(w, http.StatusOK, map[string]any{"connector_id": connector.ID, "default_on": true})
			return
		}
	}
	if err := h.addContextConfigApp(ctx, a, scope, connector.ID, userID); err != nil {
		slog.ErrorContext(ctx, "context capabilities: official app add failed", "agent_id", a.ID, "catalog_slug", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to add the official app")
		return
	}
	slog.InfoContext(ctx, "context capabilities: official app added on the configure page", "agent_id", a.ID, "workspace_id", a.WorkspaceID,
		"catalog_slug", slug, "connector_id", connector.ID, "installed", created, "scope_type", scope.ScopeType, "user_id", userID)
	writeJSON(w, http.StatusOK, map[string]any{"connector_id": connector.ID, "default_on": false})
}

// addContextConfigApp offers the connector to the agent's scopes and
// switches it on at the scope, in one transaction.
func (h *Handler) addContextConfigApp(ctx context.Context, a contextCapAgent, scope contextCapScope, connectorID, userID string) error {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := contextcap.AddOffer(ctx, tx, a.WorkspaceID, a.ID, contextcap.ResourceConnector, connectorID, userID); err != nil {
		return err
	}
	if _, err := contextcap.UpsertBinding(ctx, tx, contextcap.BindingWrite{
		WorkspaceID: a.WorkspaceID, AgentID: a.ID, ScopeType: scope.ScopeType, OrgID: a.OrgID, ScopeKey: scope.ScopeKey,
		ScopeTitle: scope.ScopeTitle, ResourceType: contextcap.ResourceConnector, ResourceID: connectorID, Enabled: true, ActorID: userID,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// contextConfigOAuthAppView is a scene's own OAuth application of an app on
// the configure page. Secrets are never returned, only whether they are set.
type contextConfigOAuthAppView struct {
	Slug        string                           `json:"slug"`
	Name        string                           `json:"name"`
	Fields      []connectorcatalog.SettingsField `json:"fields"`
	DocsURL     string                           `json:"docs_url"`
	CallbackURL string                           `json:"callback_url"`
	// Saved: the scene has its own application.
	Saved           bool   `json:"saved"`
	ClientID        string `json:"client_id"`
	ClientSecretSet bool   `json:"client_secret_set"`
	// WorkspaceReady: without its own, the scene signs in with the
	// workspace's application (saved in the admin console).
	WorkspaceReady bool `json:"workspace_ready"`
	// Ready: a sign-in can start at the scene now.
	Ready bool `json:"ready"`
	// CanEdit: the caller may save it now: the first one with the scene's
	// connect right, a saved one only as the agent's manager.
	CanEdit bool `json:"can_edit"`
}

// contextConfigOAuthAppFields are the values a scene's OAuth application
// takes: the client the sign-in uses.
var contextConfigOAuthAppFields = []connectorcatalog.SettingsField{{Key: "client_id"}, {Key: "client_secret"}}

// contextConfigOAuthAppRequest resolves a scene OAuth application request:
// the {slug} app must need one, and the scope must be a scene the caller
// may open (need). Only a scene has its own application; the workspace's
// and the enterprise's are the admin console's.
func (h *Handler) contextConfigOAuthAppRequest(w http.ResponseWriter, r *http.Request, scopeType, scopeKey, orgID string, need contextCapNeed) (string, contextCapAgent, contextCapScope, connectorcatalog.App, connectorcatalog.SettingsSpec, bool) {
	fail := func() (string, contextCapAgent, contextCapScope, connectorcatalog.App, connectorcatalog.SettingsSpec, bool) {
		return "", contextCapAgent{}, contextCapScope{}, connectorcatalog.App{}, connectorcatalog.SettingsSpec{}, false
	}
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return fail()
	}
	a, ok := h.contextCapAgentOr404(w, r)
	if !ok {
		return fail()
	}
	slug := strings.TrimSpace(chi.URLParam(r, "slug"))
	app, known := catalogApp(slug)
	spec, hasSpec := connectorcatalog.SettingsSpecFor(slug)
	if !known || !hasSpec || spec.Mode != "preregistered" {
		writeError(w, http.StatusNotFound, "this app needs no OAuth application")
		return fail()
	}
	if scopeType != contextcap.ScopeScene {
		writeErrorCode(w, http.StatusBadRequest, contextConfigErrOAuthAppSceneOnly,
			"only a scene has its own OAuth application here; the workspace's and the enterprise's are configured in the admin console")
		return fail()
	}
	if a, ok = h.contextCapRequestOrg(w, r, a, userID, contextCapScopeOrg(scopeType, scopeKey, orgID)); !ok {
		return fail()
	}
	scope, ok := h.contextCapRequireScope(w, r, a, userID, scopeType, scopeKey, need)
	if !ok {
		return fail()
	}
	return userID, a, scope, app, spec, true
}

// contextConfigSceneAppKey is the scene application key of scope.
func contextConfigSceneAppKey(a contextCapAgent, scope contextCapScope, slug string) contextcap.SceneAppKey {
	return contextcap.SceneAppKey{WorkspaceID: a.WorkspaceID, AgentID: a.ID, OrgID: a.OrgID, SceneID: scope.ScopeKey, Provider: slug}
}

func (h *Handler) contextConfigOAuthAppView(ctx context.Context, a contextCapAgent, scope contextCapScope, userID string, app connectorcatalog.App, spec connectorcatalog.SettingsSpec) (contextConfigOAuthAppView, error) {
	view := contextConfigOAuthAppView{
		Slug: app.Slug, Name: app.Name, Fields: contextConfigOAuthAppFields, DocsURL: spec.DocsURL,
		CallbackURL:    connectorcatalog.ProductionCallbackURL,
		WorkspaceReady: h.catalogOAuthAvailableFor(ctx, a.WorkspaceID, app),
	}
	saved, err := contextcap.GetSceneApp(ctx, h.DB, contextConfigSceneAppKey(a, scope, app.Slug))
	switch {
	case errors.Is(err, contextcap.ErrNotFound):
		view.CanEdit = scope.Rights.Connect
	case err != nil:
		return view, err
	default:
		view.Saved, view.ClientID, view.ClientSecretSet = true, saved.ClientID, len(saved.SecretCiphertext) > 0
		manages, err := h.contextCapManages(ctx, a, userID)
		if err != nil {
			return view, err
		}
		view.CanEdit = manages
	}
	// The binding a connection at this scene stores into (see
	// StartContextConfigConnection).
	binding := contextcap.CredentialBinding{WorkspaceID: a.WorkspaceID, AgentID: a.ID, ScopeType: contextcap.ScopeScene, OrgID: a.OrgID, ScopeKey: scope.ScopeKey}
	view.Ready = h.connectorOAuthDeploymentErrorForScope(ctx, binding, app) == nil
	return view, nil
}

// GetContextConfigOAuthApp: GET /api/context-capabilities/agents/{agentId}/apps/{slug}/oauth-app?scope_type=scene&scope_key=&org_id=
func (h *Handler) GetContextConfigOAuthApp(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	userID, a, scope, app, spec, ok := h.contextConfigOAuthAppRequest(w, r, query.Get("scope_type"), query.Get("scope_key"), query.Get("org_id"), contextCapNeedRead)
	if !ok {
		return
	}
	view, err := h.contextConfigOAuthAppView(r.Context(), a, scope, userID, app, spec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OAuth application lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// PutContextConfigOAuthApp saves a scene's own OAuth application of the app:
// PUT /api/context-capabilities/agents/{agentId}/apps/{slug}/oauth-app
// {scope_type: "scene", scope_key, org_id?, client_id, client_secret?}.
// The first one needs the scene's connect right (冬翔, 2026-10-02: a scene's
// members configure its OAuth application); changing a saved one is for the
// agent's managers only (403 oauth_app_locked). An omitted secret keeps the
// stored one.
func (h *Handler) PutContextConfigOAuthApp(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ScopeType    string `json:"scope_type"`
		ScopeKey     string `json:"scope_key"`
		OrgID        string `json:"org_id"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if !decodeContextCapBody(w, r, contextConfigAppBodyLimit, &input) {
		return
	}
	userID, a, scope, app, spec, ok := h.contextConfigOAuthAppRequest(w, r, input.ScopeType, input.ScopeKey, input.OrgID, contextCapNeedCredential)
	if !ok {
		return
	}
	ctx := r.Context()
	clientID, secret := strings.TrimSpace(input.ClientID), strings.TrimSpace(input.ClientSecret)
	key := contextConfigSceneAppKey(a, scope, app.Slug)
	saved, err := contextcap.GetSceneApp(ctx, h.DB, key)
	if err != nil && !errors.Is(err, contextcap.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "OAuth application lookup failed")
		return
	}
	exists := err == nil
	if exists {
		manages, err := h.contextCapManages(ctx, a, userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "manager lookup failed")
			return
		}
		if !manages {
			writeErrorCode(w, http.StatusForbidden, contextConfigErrOAuthAppLocked,
				"this scene's OAuth application is saved; only the agent's managers change it")
			return
		}
	}
	// A new client ID needs its own secret: a stored one belongs to the
	// previous client.
	if clientID == "" || (secret == "" && !(exists && len(saved.SecretCiphertext) > 0 && clientID == saved.ClientID)) {
		writeErrorCode(w, http.StatusBadRequest, "oauth_app_field_required", "client_id and client_secret are required")
		return
	}
	var ciphertext []byte
	var hint string
	if secret != "" {
		if ciphertext, hint, err = h.sealConnectorAppSecret(secret); err != nil {
			writeConnectorConfigErrorStatus(w, err)
			return
		}
	}
	if exists {
		err = contextcap.UpdateSceneApp(ctx, h.DB, key, clientID, ciphertext, hint, userID)
	} else {
		err = contextcap.CreateSceneApp(ctx, h.DB, key, clientID, ciphertext, hint, userID)
	}
	switch {
	case errors.Is(err, contextcap.ErrSceneAppExists):
		// Someone saved it first; it is the managers' to change now.
		writeErrorCode(w, http.StatusConflict, contextConfigErrOAuthAppLocked, "this scene's OAuth application was just saved")
		return
	case err != nil:
		slog.ErrorContext(ctx, "context capabilities: scene OAuth application save failed", "agent_id", a.ID, "catalog_slug", app.Slug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save the OAuth application")
		return
	}
	slog.InfoContext(ctx, "context capabilities: scene OAuth application saved", "agent_id", a.ID, "workspace_id", a.WorkspaceID,
		"catalog_slug", app.Slug, "scene_id", scope.ScopeKey, "changed", exists, "user_id", userID)
	view, err := h.contextConfigOAuthAppView(ctx, a, scope, userID, app, spec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OAuth application lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// DeleteContextConfigOAuthApp removes a scene's own OAuth application, so the
// scene signs in with the workspace's again:
// DELETE /api/context-capabilities/agents/{agentId}/apps/{slug}/oauth-app?scope_type=scene&scope_key=&org_id=
// The agent's managers only (403 oauth_app_locked). Accounts it authorized
// keep their tokens until a refresh needs the removed client, then ask to
// reconnect. 204 also when none was saved.
func (h *Handler) DeleteContextConfigOAuthApp(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	userID, a, scope, app, _, ok := h.contextConfigOAuthAppRequest(w, r, query.Get("scope_type"), query.Get("scope_key"), query.Get("org_id"), contextCapNeedRead)
	if !ok {
		return
	}
	ctx := r.Context()
	manages, err := h.contextCapManages(ctx, a, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "manager lookup failed")
		return
	}
	if !manages {
		writeErrorCode(w, http.StatusForbidden, contextConfigErrOAuthAppLocked, "only the agent's managers remove a scene's OAuth application")
		return
	}
	removed, err := contextcap.DeleteSceneApp(ctx, h.DB, contextConfigSceneAppKey(a, scope, app.Slug))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove the OAuth application")
		return
	}
	if removed {
		slog.InfoContext(ctx, "context capabilities: scene OAuth application removed", "agent_id", a.ID, "workspace_id", a.WorkspaceID,
			"catalog_slug", app.Slug, "scene_id", scope.ScopeKey, "user_id", userID)
	}
	w.WriteHeader(http.StatusNoContent)
}
