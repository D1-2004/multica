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
	"github.com/multica-ai/multica/server/internal/connectorconfig"
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
//   - GET|PUT …/apps/{slug}/oauth-app reads and saves the OAuth application
//     an app without dynamic registration needs (Slack, Asana, GitHub when
//     the deployment has no GitHub App), with the callback URL to register
//     in the provider's console. Anyone who may open the agent here reads
//     it and saves the first one. It is the workspace's client, shared by
//     every connection and token refresh, so changing (or re-enabling) a
//     saved one is for workspace owners and admins (403 app_requires_admin).

const contextConfigAppBodyLimit = 64 << 10

// Refusal codes of the configure page's official app routes.
const (
	contextConfigErrAppRequiresAdmin = "app_requires_admin"
	contextConfigErrAppDisabled      = "app_disabled"
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

// contextConfigOAuthAppView is an app's OAuth application on the configure
// page. Secrets are never returned, only whether they are set.
type contextConfigOAuthAppView struct {
	Slug             string                           `json:"slug"`
	Name             string                           `json:"name"`
	Fields           []connectorcatalog.SettingsField `json:"fields"`
	DocsURL          string                           `json:"docs_url"`
	CallbackURL      string                           `json:"callback_url"`
	Ready            bool                             `json:"ready"`
	ClientID         string                           `json:"client_id"`
	ClientSecretSet  bool                             `json:"client_secret_set"`
	AppID            string                           `json:"app_id"`
	AppSlug          string                           `json:"app_slug"`
	PrivateKeySet    bool                             `json:"private_key_set"`
	OptionalSecret   bool                             `json:"optional_secret_set"`
	DeploymentClient bool                             `json:"deployment_client"`
	// Saved: the workspace has one (changing it is for workspace admins).
	Saved bool `json:"saved"`
}

// contextConfigOAuthAppScope authorizes a caller who may open the agent on
// the configure page (a grant, or managing it) for the {slug} app that needs
// an OAuth application, and says whether the caller is a workspace admin.
func (h *Handler) contextConfigOAuthAppScope(w http.ResponseWriter, r *http.Request) (string, bool, contextCapAgent, connectorcatalog.App, connectorcatalog.SettingsSpec, bool) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return "", false, contextCapAgent{}, connectorcatalog.App{}, connectorcatalog.SettingsSpec{}, false
	}
	a, ok := h.contextCapAgentOr404(w, r)
	if !ok {
		return "", false, contextCapAgent{}, connectorcatalog.App{}, connectorcatalog.SettingsSpec{}, false
	}
	slug := strings.TrimSpace(chi.URLParam(r, "slug"))
	app, known := catalogApp(slug)
	spec, hasSpec := connectorcatalog.SettingsSpecFor(slug)
	if !known || !hasSpec || spec.Mode != "preregistered" {
		writeError(w, http.StatusNotFound, "this app needs no OAuth application")
		return "", false, contextCapAgent{}, connectorcatalog.App{}, connectorcatalog.SettingsSpec{}, false
	}
	access, err := h.contextCapHasAccess(r.Context(), a, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "grant lookup failed")
		return "", false, contextCapAgent{}, connectorcatalog.App{}, connectorcatalog.SettingsSpec{}, false
	}
	if !access {
		writeError(w, http.StatusForbidden, contextCapForbiddenAgent)
		return "", false, contextCapAgent{}, connectorcatalog.App{}, connectorcatalog.SettingsSpec{}, false
	}
	admin, err := h.contextCapWorkspaceAdmin(r.Context(), a, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "member lookup failed")
		return "", false, contextCapAgent{}, connectorcatalog.App{}, connectorcatalog.SettingsSpec{}, false
	}
	return userID, admin, a, app, spec, true
}

// contextConfigOAuthAppRecord is the workspace's OAuth application of the
// app: the enabled one sign-ins use, else the newest disabled one, else
// none (ErrNotFound).
func (h *Handler) contextConfigOAuthAppRecord(ctx context.Context, workspaceID, slug string) (connectorconfig.App, error) {
	if saved, err := connectorconfig.OldestEnabled(ctx, h.DB, workspaceID, slug); err == nil {
		return saved, nil
	} else if !errors.Is(err, connectorconfig.ErrNotFound) {
		return connectorconfig.App{}, err
	}
	apps, err := connectorconfig.List(ctx, h.DB, workspaceID)
	if err != nil {
		return connectorconfig.App{}, err
	}
	for i := len(apps) - 1; i >= 0; i-- {
		if apps[i].Provider == slug {
			return apps[i], nil
		}
	}
	return connectorconfig.App{}, connectorconfig.ErrNotFound
}

func (h *Handler) contextConfigOAuthAppView(ctx context.Context, workspaceID string, app connectorcatalog.App, spec connectorcatalog.SettingsSpec) (contextConfigOAuthAppView, error) {
	view := contextConfigOAuthAppView{
		Slug: app.Slug, Name: app.Name, Fields: spec.Fields, DocsURL: spec.DocsURL,
		CallbackURL: connectorcatalog.ProductionCallbackURL, Ready: h.catalogOAuthAvailableFor(ctx, workspaceID, app),
		DeploymentClient: connectorAppEnvConfigured(app.Slug),
	}
	if view.Fields == nil {
		view.Fields = []connectorcatalog.SettingsField{}
	}
	saved, err := h.contextConfigOAuthAppRecord(ctx, workspaceID, app.Slug)
	switch {
	case errors.Is(err, connectorconfig.ErrNotFound) || errors.Is(err, connectorconfig.ErrSchemaMissing):
		if view.ClientID == "" {
			view.ClientID = spec.KnownClientID
		}
		return view, nil
	case err != nil:
		return view, err
	}
	view.Saved = true
	if saved.CallbackMode == connectorconfig.CallbackSelf {
		// A self callback is registered on this deployment's own origin.
		view.CallbackURL = h.connectorOAuthAppOrigin() + connectorOAuthCallbackPath
	}
	view.ClientID, view.ClientSecretSet = saved.ClientID, len(saved.SecretCiphertext) > 0
	view.AppID, view.AppSlug = saved.AppIdentifier, saved.InstallSlug
	view.PrivateKeySet, view.OptionalSecret = len(saved.PrivateKeyCiphertext) > 0, len(saved.OptionalCiphertext) > 0
	return view, nil
}

// GetContextConfigOAuthApp: GET /api/context-capabilities/agents/{agentId}/apps/{slug}/oauth-app.
func (h *Handler) GetContextConfigOAuthApp(w http.ResponseWriter, r *http.Request) {
	_, _, a, app, spec, ok := h.contextConfigOAuthAppScope(w, r)
	if !ok {
		return
	}
	view, err := h.contextConfigOAuthAppView(r.Context(), a.WorkspaceID, app, spec)
	if err != nil {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// PutContextConfigOAuthApp saves the app's OAuth application and enables it:
// PUT /api/context-capabilities/agents/{agentId}/apps/{slug}/oauth-app
// {client_id, client_secret?, app_id?, app_slug?, private_key?,
// optional_secret?}. An omitted secret keeps the stored one.
func (h *Handler) PutContextConfigOAuthApp(w http.ResponseWriter, r *http.Request) {
	userID, admin, a, app, spec, ok := h.contextConfigOAuthAppScope(w, r)
	if !ok {
		return
	}
	var input struct {
		ClientID       string `json:"client_id"`
		ClientSecret   string `json:"client_secret"`
		AppID          string `json:"app_id"`
		AppSlug        string `json:"app_slug"`
		PrivateKey     string `json:"private_key"`
		OptionalSecret string `json:"optional_secret"`
	}
	if !decodeContextCapBody(w, r, contextConfigAppBodyLimit, &input) {
		return
	}
	ctx := r.Context()
	saved, savedErr := h.contextConfigOAuthAppRecord(ctx, a.WorkspaceID, app.Slug)
	if savedErr != nil && !errors.Is(savedErr, connectorconfig.ErrNotFound) {
		writeConnectorConfigErrorStatus(w, savedErr)
		return
	}
	exists := savedErr == nil
	if exists && !admin {
		writeErrorCode(w, http.StatusForbidden, contextConfigErrAppRequiresAdmin,
			"the workspace already has this app's OAuth application; only workspace owners and admins change it")
		return
	}
	// Every value the app requires: given now, or stored before.
	for _, field := range spec.Fields {
		if field.Optional {
			continue
		}
		given, stored := "", ""
		switch field.Key {
		case "client_id":
			given, stored = input.ClientID, saved.ClientID
		case "client_secret":
			given = input.ClientSecret
			if exists && len(saved.SecretCiphertext) > 0 {
				stored = "set"
			}
		case "app_id":
			given, stored = input.AppID, saved.AppIdentifier
		case "app_slug":
			given, stored = input.AppSlug, saved.InstallSlug
		case "private_key":
			given = input.PrivateKey
			if exists && len(saved.PrivateKeyCiphertext) > 0 {
				stored = "set"
			}
		default:
			continue
		}
		if strings.TrimSpace(given) == "" && stored == "" {
			writeErrorCode(w, http.StatusBadRequest, "oauth_app_field_required", field.Key+" is required")
			return
		}
	}
	enabled := true
	in := connectorconfig.AppInput{
		Provider: app.Slug, DisplayName: app.Name, ClientID: strings.TrimSpace(input.ClientID), Enabled: &enabled,
		AppIdentifier: strings.TrimSpace(input.AppID), InstallSlug: strings.TrimSpace(input.AppSlug),
	}
	if exists {
		// An admin's callback mode stays; the preset only fills an empty one.
		in.CallbackMode = saved.CallbackMode
	}
	applySettingsPreset(&in)
	if err := h.sealConnectorAppExtras(&in, input.PrivateKey, input.OptionalSecret); err != nil {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	secret := strings.TrimSpace(input.ClientSecret)
	in.ClientSecret = secret
	var err error
	switch {
	case !exists:
		if in.ClientID == "" || secret == "" {
			writeError(w, http.StatusBadRequest, "client_id and client_secret are required")
			return
		}
		ciphertext, hint, sealErr := h.sealConnectorAppSecret(secret)
		if sealErr != nil {
			writeConnectorConfigErrorStatus(w, sealErr)
			return
		}
		if _, err := connectorconfig.Create(ctx, h.DB, a.WorkspaceID, userID, in, ciphertext, hint); err != nil {
			writeConnectorConfigErrorStatus(w, err)
			return
		}
	default:
		if in.ClientID == "" {
			in.ClientID = saved.ClientID
		}
		if in.AppIdentifier == "" {
			in.AppIdentifier = saved.AppIdentifier
		}
		if in.InstallSlug == "" {
			in.InstallSlug = saved.InstallSlug
		}
		var ciphertext []byte
		var hint string
		if secret != "" {
			if ciphertext, hint, err = h.sealConnectorAppSecret(secret); err != nil {
				writeConnectorConfigErrorStatus(w, err)
				return
			}
		} else if len(saved.SecretCiphertext) == 0 {
			writeError(w, http.StatusBadRequest, "client_secret is required")
			return
		}
		if _, err := connectorconfig.Update(ctx, h.DB, a.WorkspaceID, saved.ID, in, ciphertext, hint, secret != ""); err != nil {
			writeConnectorConfigErrorStatus(w, err)
			return
		}
	}
	slog.InfoContext(ctx, "context capabilities: OAuth application saved on the configure page", "agent_id", a.ID, "workspace_id", a.WorkspaceID,
		"catalog_slug", app.Slug, "user_id", userID)
	view, err := h.contextConfigOAuthAppView(ctx, a.WorkspaceID, app, spec)
	if err != nil {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
