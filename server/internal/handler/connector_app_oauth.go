package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/connectorcatalog"
	"github.com/multica-ai/multica/server/internal/connectorconfig"
	"github.com/multica-ai/multica/server/internal/contextcap"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// githubOAuthClient is the OAuth client used for the GitHub catalog app.
// A workspace application replaces the environment variables. An enabled
// application whose secret cannot be opened is an error: the environment
// client is a different registration and must not be substituted.
type githubOAuthClient struct {
	Source                string
	AppID                 string
	ClientID              string
	ClientSecret          string
	Scopes                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	CallbackMode          string
	// Scene: the scene's own application (context_connector_app), saved on
	// the configure page. Its Source reads as a saved workspace application.
	Scene bool
}

func (h *Handler) githubOAuthClient(ctx context.Context, workspaceID string) (githubOAuthClient, error) {
	app, err := connectorconfig.OldestEnabled(ctx, h.DB, workspaceID, "github")
	if err != nil {
		if errors.Is(err, connectorconfig.ErrNotFound) || errors.Is(err, connectorconfig.ErrSchemaMissing) || errors.Is(err, connectorconfig.ErrInvalid) {
			return envGitHubOAuthClient(), nil
		}
		slog.WarnContext(ctx, "connector app lookup failed; using environment GitHub client", "workspace_id", workspaceID, "error", err)
		return envGitHubOAuthClient(), nil
	}
	secret, err := connectorconfig.OpenString(h.InternalConnectorSecretBox, app.SecretCiphertext)
	if err != nil {
		return githubOAuthClient{}, connectorconfig.ErrSecretUnavailable
	}
	client := githubOAuthClient{
		Source: connectorconfig.SourceWorkspace, AppID: app.ID, ClientID: app.ClientID, ClientSecret: secret,
		Scopes: app.Scopes, AuthorizationEndpoint: app.AuthorizationEndpoint, TokenEndpoint: app.TokenEndpoint,
		CallbackMode: app.CallbackMode,
	}
	if client.CallbackMode == "" {
		client.CallbackMode = connectorconfig.CallbackProductionForward
	}
	return client, nil
}

func envGitHubOAuthClient() githubOAuthClient {
	return githubOAuthClient{
		Source:       connectorconfig.SourceEnv,
		ClientID:     strings.TrimSpace(os.Getenv("GITHUB_APP_CLIENT_ID")),
		ClientSecret: os.Getenv("GITHUB_APP_CLIENT_SECRET"),
		CallbackMode: connectorconfig.CallbackProductionForward,
	}
}

// githubOAuthRedirectOrigin chooses the callback origin. self stays on this
// deployment. production_forward keeps the existing pre-release redirect to
// the production origin.
func githubOAuthRedirectOrigin(homeOrigin, forwardedOrigin, mode string) string {
	if mode == connectorconfig.CallbackSelf {
		return homeOrigin
	}
	return forwardedOrigin
}

func connectorAppEnvConfigured(provider string) bool {
	if provider != "github" {
		return false
	}
	return strings.TrimSpace(os.Getenv("GITHUB_APP_CLIENT_ID")) != "" && os.Getenv("GITHUB_APP_CLIENT_SECRET") != ""
}

// preregisteredOAuthClient is the workspace's confidential client for an
// app that cannot dynamically register (Asana). An empty client means the
// workspace has not saved one. A saved secret that cannot be opened is an
// error and does not fall through to another registration.
func (h *Handler) preregisteredOAuthClient(ctx context.Context, workspaceID string, app connectorcatalog.App) (githubOAuthClient, error) {
	saved, err := connectorconfig.OldestEnabled(ctx, h.DB, workspaceID, app.Slug)
	if err != nil {
		if errors.Is(err, connectorconfig.ErrNotFound) || errors.Is(err, connectorconfig.ErrSchemaMissing) || errors.Is(err, connectorconfig.ErrInvalid) {
			return githubOAuthClient{}, nil
		}
		return githubOAuthClient{}, err
	}
	secret, err := connectorconfig.OpenString(h.InternalConnectorSecretBox, saved.SecretCiphertext)
	if err != nil {
		return githubOAuthClient{}, connectorconfig.ErrSecretUnavailable
	}
	client := githubOAuthClient{
		Source: connectorconfig.SourceWorkspace, AppID: saved.ID, ClientID: saved.ClientID, ClientSecret: secret,
		Scopes: saved.Scopes, AuthorizationEndpoint: saved.AuthorizationEndpoint, TokenEndpoint: saved.TokenEndpoint,
		CallbackMode: saved.CallbackMode,
	}
	if client.AuthorizationEndpoint == "" {
		client.AuthorizationEndpoint = app.AuthorizationEndpoint
	}
	if client.TokenEndpoint == "" {
		client.TokenEndpoint = app.TokenEndpoint
	}
	if client.Scopes == "" {
		client.Scopes = app.Scope
	}
	if client.CallbackMode == "" {
		client.CallbackMode = connectorconfig.CallbackProductionForward
	}
	return client, nil
}

// sceneOAuthClient is the scene's own OAuth application of app for a
// connection or credential at binding. ok is false outside a scene scope,
// for an app that registers dynamically, and when the scene saved none. A
// saved secret that cannot be opened is an error: the workspace client is a
// different registration and must not be substituted.
func (h *Handler) sceneOAuthClient(ctx context.Context, binding contextcap.CredentialBinding, app connectorcatalog.App) (githubOAuthClient, bool, error) {
	key, ok := contextcap.SceneAppKeyOf(binding, app.Slug)
	if !ok {
		return githubOAuthClient{}, false, nil
	}
	spec, ok := connectorcatalog.SettingsSpecFor(app.Slug)
	if !ok || spec.Mode != "preregistered" {
		return githubOAuthClient{}, false, nil
	}
	saved, err := contextcap.GetSceneApp(ctx, h.DB, key)
	if errors.Is(err, contextcap.ErrNotFound) {
		return githubOAuthClient{}, false, nil
	}
	if err != nil {
		return githubOAuthClient{}, false, err
	}
	if h.InternalConnectorSecretBox == nil {
		return githubOAuthClient{}, false, connectorconfig.ErrSecretUnavailable
	}
	secret, err := connectorconfig.OpenString(h.InternalConnectorSecretBox, saved.SecretCiphertext)
	if err != nil {
		return githubOAuthClient{}, false, connectorconfig.ErrSecretUnavailable
	}
	client := githubOAuthClient{
		Source: connectorconfig.SourceWorkspace, Scene: true, AppID: saved.ID, ClientID: saved.ClientID, ClientSecret: secret,
		Scopes: spec.Scopes, AuthorizationEndpoint: spec.AuthorizationEndpoint, TokenEndpoint: spec.TokenEndpoint,
		CallbackMode: connectorconfig.CallbackProductionForward,
	}
	if client.AuthorizationEndpoint == "" {
		client.AuthorizationEndpoint = app.AuthorizationEndpoint
	}
	if client.TokenEndpoint == "" {
		client.TokenEndpoint = app.TokenEndpoint
	}
	if client.Scopes == "" {
		client.Scopes = app.Scope
	}
	return client, true, nil
}

// connectorOAuthScopeBinding is the credential binding a connection at scope
// stores into (empty for the workspace scope).
func connectorOAuthScopeBinding(scope connectorOAuthScope) contextcap.CredentialBinding {
	if scope.ScopeType == connectorOAuthScopeWorkspace {
		return contextcap.CredentialBinding{}
	}
	return contextcap.CredentialBinding{
		WorkspaceID: scope.WorkspaceID, AgentID: scope.AgentID, ConnectorID: scope.ConnectorID,
		ScopeType: scope.ScopeType, OrgID: scope.OrgID, ScopeKey: scope.ScopeKey,
	}
}

// connectorOAuthDeploymentErrorForScope is connectorOAuthDeploymentErrorFor
// for a connection at binding: a scene with its own OAuth application of
// app needs only credential storage and the app origin.
func (h *Handler) connectorOAuthDeploymentErrorForScope(ctx context.Context, binding contextcap.CredentialBinding, app connectorcatalog.App) error {
	if _, ok, err := h.sceneOAuthClient(ctx, binding, app); err != nil {
		return oauthStartError(http.StatusServiceUnavailable, connectOAuthErrOAuthNotEnabled, "the scene's OAuth application is unavailable")
	} else if ok {
		if h.InternalConnectorSecretBox == nil {
			return oauthStartError(http.StatusServiceUnavailable, "credential_storage_unavailable", "connector credential storage is not configured")
		}
		if h.connectorOAuthAppOrigin() == "" {
			return oauthStartError(http.StatusServiceUnavailable, "app_origin_missing", "no app origin is configured")
		}
		return nil
	}
	return h.connectorOAuthDeploymentErrorFor(ctx, binding.WorkspaceID, app)
}

func (h *Handler) connectorOAuthDeploymentErrorFor(ctx context.Context, workspaceID string, app connectorcatalog.App) error {
	if h.InternalConnectorSecretBox == nil {
		return oauthStartError(http.StatusServiceUnavailable, "credential_storage_unavailable", "connector credential storage is not configured")
	}
	switch app.AuthKind {
	case connectorcatalog.AuthOAuthGitHubApp:
		client, err := h.githubOAuthClient(ctx, workspaceID)
		if err != nil || strings.TrimSpace(client.ClientID) == "" || client.ClientSecret == "" {
			return oauthStartError(http.StatusServiceUnavailable, connectOAuthErrOAuthNotEnabled, "OAuth is not configured for this app")
		}
	case connectorcatalog.AuthOAuthPreregistered:
		client, err := h.preregisteredOAuthClient(ctx, workspaceID, app)
		if err != nil || strings.TrimSpace(client.ClientID) == "" || client.ClientSecret == "" {
			return oauthStartError(http.StatusServiceUnavailable, connectOAuthErrOAuthNotEnabled, "OAuth is not configured for this app")
		}
	default:
		if !app.OAuthAvailable(githubUserAuthorizationConfigured()) {
			return oauthStartError(http.StatusServiceUnavailable, connectOAuthErrOAuthNotEnabled, "OAuth is not configured for this app")
		}
	}
	if h.connectorOAuthAppOrigin() == "" {
		return oauthStartError(http.StatusServiceUnavailable, "app_origin_missing", "no app origin is configured")
	}
	return nil
}

func (h *Handler) catalogOAuthAvailableFor(ctx context.Context, workspaceID string, app connectorcatalog.App) bool {
	return h.connectorOAuthDeploymentErrorFor(ctx, workspaceID, app) == nil
}

func (h *Handler) catalogAppFactsFor(ctx context.Context, workspaceID string, app connectorcatalog.App) catalogAppFacts {
	facts := h.catalogAppFactsView(app)
	facts.OAuthAvailable = h.catalogOAuthAvailableFor(ctx, workspaceID, app)
	return facts
}

// applyAuthInstanceCredential selects the authorization instance bound to
// this task. A match is final: a missing token does not fall through to
// another account or to the legacy layers. No match leaves those layers
// alone. Lookup failures before a match also leave them alone, so a deploy
// that has not migrated yet keeps the previous behavior.
func (h *Handler) applyAuthInstanceCredential(ctx context.Context, c *internalConnector, task db.AgentTaskQueue) (decided, ok bool) {
	if c.AuthMode != "oauth" || c.CatalogSlug == "" {
		return false, false
	}
	app, records, err := connectorconfig.InstancesForProvider(ctx, h.DB, c.WorkspaceID, c.CatalogSlug)
	if err != nil {
		if !errors.Is(err, connectorconfig.ErrNotFound) && !errors.Is(err, connectorconfig.ErrSchemaMissing) && !errors.Is(err, connectorconfig.ErrInvalid) {
			slog.WarnContext(ctx, "connector auth instance lookup failed; keeping legacy credentials", "connector_id", c.ID, "error", err)
		}
		return false, false
	}
	instances := make([]connectorconfig.Instance, len(records))
	byID := make(map[string]connectorconfig.InstanceRecord, len(records))
	for i, rec := range records {
		instances[i] = rec.Instance
		byID[rec.ID] = rec
	}
	picked, rank, matched := connectorconfig.Select(instances, connectorconfig.CallContext{
		AgentID:     uuidToString(task.AgentID),
		ProjectID:   h.issueProjectID(ctx, c.WorkspaceID, task.IssueID),
		Environment: deploymentEnvironmentName(),
	})
	if !matched {
		return false, false
	}
	rec := byID[picked.ID]
	secret, openErr := connectorconfig.OpenToken(h.InternalConnectorSecretBox, rec.TokenCiphertext)
	if openErr != nil || !secret.Usable(time.Now()) {
		slog.InfoContext(ctx, "connector auth instance matched without a usable token",
			"connector_id", c.ID, "app_id", app.ID, "instance_id", picked.ID, "scope_kind", rank)
		return true, false
	}
	c.setResolvedSecret(secret, "auth_instance:"+rank, contextcap.CredentialBinding{})
	return true, true
}

func (h *Handler) issueProjectID(ctx context.Context, workspaceID string, issueID pgtype.UUID) string {
	if !issueID.Valid || workspaceID == "" {
		return ""
	}
	var projectID string
	err := h.DB.QueryRow(ctx, `SELECT COALESCE(project_id::text, '') FROM issue WHERE workspace_id = $1::uuid AND id = $2::uuid`, workspaceID, uuidToString(issueID)).Scan(&projectID)
	if err != nil {
		return ""
	}
	return projectID
}

// attachOAuthTokenToAuthInstance writes the access token from a completed
// catalog connect onto the newest pending authorization instance. The
// settings page creates that instance before redirecting. A connect that
// did not create one leaves the shared connector credential as the only
// credential, so an ordinary catalog connect does not shadow it. The token
// is the instance credential the runtime uses when that instance is
// selected. Failure here does not undo the connector credential that was
// just stored.
func (h *Handler) attachOAuthTokenToAuthInstance(ctx context.Context, workspaceID, provider, account, accessToken string) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" || h.InternalConnectorSecretBox == nil || provider == "" {
		return
	}
	app, records, err := connectorconfig.InstancesForProvider(ctx, h.DB, workspaceID, provider)
	if err != nil {
		return
	}
	ciphertext, hint, err := h.sealConnectorInstanceToken(accessToken)
	if err != nil {
		slog.WarnContext(ctx, "connector auth instance token was not stored", "provider", provider, "error", err)
		return
	}
	account = strings.TrimSpace(account)
	if len(account) > 128 {
		account = account[:128]
	}
	for i := len(records) - 1; i >= 0; i-- {
		rec := records[i]
		if !rec.Enabled || rec.Status != connectorconfig.StatusPending || len(rec.TokenCiphertext) > 0 {
			continue
		}
		login := account
		if login == "" {
			login = rec.ExternalLogin
		}
		enabled := true
		_, err = connectorconfig.UpdateInstance(ctx, h.DB, workspaceID, app.ID, rec.ID, connectorconfig.InstanceInput{
			Label: rec.Label, ExternalSubject: rec.ExternalSubject, ExternalLogin: login,
			Status: connectorconfig.StatusActive, Enabled: &enabled,
		}, ciphertext, hint, true)
		if err != nil {
			slog.WarnContext(ctx, "connector auth instance token was not stored", "provider", provider, "error", err)
		}
		return
	}
}

func deploymentEnvironmentName() string {
	for _, name := range []string{"AONE_ENV_TYPE", "ENV_TYPE", "APP_ENV"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return connectorconfig.NormalizeEnvironment(value)
		}
	}
	return ""
}
