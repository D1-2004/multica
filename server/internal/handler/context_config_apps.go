package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/connectorcatalog"
	"github.com/multica-ai/multica/server/internal/connectorconfig"
	"github.com/multica-ai/multica/server/internal/contextcap"
)

// Official apps on the configure page: a level adds any catalog app and
// completes its connection there, without the admin console.
//
//   - POST …/apps/{slug} adds the app at a level: the workspace's catalog
//     connector is installed when missing, offered to the agent's scopes
//     when it is not the agent's own, and switched on at the level. It needs
//     the level's switch right, as switching an offered item does.
//   - GET|PUT …/apps/{slug}/oauth-app reads and saves the OAuth application
//     an app without dynamic registration needs (Slack, Asana, GitHub when
//     the deployment has no GitHub App), with the callback URL to register
//     in the provider's console. It is the workspace's confidential client,
//     so only the agent's managers read or change it here.

const contextConfigAppBodyLimit = 64 << 10

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
	connector, created, err := h.createCatalogConnector(ctx, a.WorkspaceID, slug)
	if err != nil {
		slog.ErrorContext(ctx, "context capabilities: official app install failed", "agent_id", a.ID, "catalog_slug", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to add the official app")
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
}

// contextConfigOAuthAppScope authorizes a manager of the agent for the
// {slug} app that needs an OAuth application.
func (h *Handler) contextConfigOAuthAppScope(w http.ResponseWriter, r *http.Request) (string, contextCapAgent, connectorcatalog.App, connectorcatalog.SettingsSpec, bool) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return "", contextCapAgent{}, connectorcatalog.App{}, connectorcatalog.SettingsSpec{}, false
	}
	a, ok := h.contextCapAgentOr404(w, r)
	if !ok {
		return "", contextCapAgent{}, connectorcatalog.App{}, connectorcatalog.SettingsSpec{}, false
	}
	slug := strings.TrimSpace(chi.URLParam(r, "slug"))
	app, known := catalogApp(slug)
	spec, hasSpec := connectorcatalog.SettingsSpecFor(slug)
	if !known || !hasSpec || spec.Mode != "preregistered" {
		writeError(w, http.StatusNotFound, "this app needs no OAuth application")
		return "", contextCapAgent{}, connectorcatalog.App{}, connectorcatalog.SettingsSpec{}, false
	}
	manages, err := h.contextCapManages(r.Context(), a, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "manager lookup failed")
		return "", contextCapAgent{}, connectorcatalog.App{}, connectorcatalog.SettingsSpec{}, false
	}
	if !manages {
		writeErrorCode(w, http.StatusForbidden, contextCapErrManagerOnly, "only the agent's managers configure an app's OAuth application")
		return "", contextCapAgent{}, connectorcatalog.App{}, connectorcatalog.SettingsSpec{}, false
	}
	return userID, a, app, spec, true
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
	view.ClientID, view.ClientSecretSet = saved.ClientID, len(saved.SecretCiphertext) > 0
	view.AppID, view.AppSlug = saved.AppIdentifier, saved.InstallSlug
	view.PrivateKeySet, view.OptionalSecret = len(saved.PrivateKeyCiphertext) > 0, len(saved.OptionalCiphertext) > 0
	return view, nil
}

// GetContextConfigOAuthApp: GET /api/context-capabilities/agents/{agentId}/apps/{slug}/oauth-app.
func (h *Handler) GetContextConfigOAuthApp(w http.ResponseWriter, r *http.Request) {
	_, a, app, spec, ok := h.contextConfigOAuthAppScope(w, r)
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
	userID, a, app, spec, ok := h.contextConfigOAuthAppScope(w, r)
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
	enabled := true
	in := connectorconfig.AppInput{
		Provider: app.Slug, DisplayName: app.Name, ClientID: strings.TrimSpace(input.ClientID), Enabled: &enabled,
		AppIdentifier: strings.TrimSpace(input.AppID), InstallSlug: strings.TrimSpace(input.AppSlug),
	}
	applySettingsPreset(&in)
	if err := h.sealConnectorAppExtras(&in, input.PrivateKey, input.OptionalSecret); err != nil {
		writeConnectorConfigErrorStatus(w, err)
		return
	}
	secret := strings.TrimSpace(input.ClientSecret)
	in.ClientSecret = secret
	saved, err := h.contextConfigOAuthAppRecord(ctx, a.WorkspaceID, app.Slug)
	switch {
	case errors.Is(err, connectorconfig.ErrNotFound):
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
	case err != nil:
		writeConnectorConfigErrorStatus(w, err)
		return
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
