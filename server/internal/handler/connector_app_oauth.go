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

func (h *Handler) connectorOAuthDeploymentErrorFor(ctx context.Context, workspaceID string, app connectorcatalog.App) error {
	if h.InternalConnectorSecretBox == nil {
		return oauthStartError(http.StatusServiceUnavailable, "credential_storage_unavailable", "connector credential storage is not configured")
	}
	if app.AuthKind == connectorcatalog.AuthOAuthGitHubApp {
		client, err := h.githubOAuthClient(ctx, workspaceID)
		if err != nil || strings.TrimSpace(client.ClientID) == "" || client.ClientSecret == "" {
			return oauthStartError(http.StatusServiceUnavailable, connectOAuthErrOAuthNotEnabled, "OAuth is not configured for this app")
		}
	} else if !app.OAuthAvailable(githubUserAuthorizationConfigured()) {
		return oauthStartError(http.StatusServiceUnavailable, connectOAuthErrOAuthNotEnabled, "OAuth is not configured for this app")
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

func deploymentEnvironmentName() string {
	for _, name := range []string{"AONE_ENV_TYPE", "ENV_TYPE", "APP_ENV"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return connectorconfig.NormalizeEnvironment(value)
		}
	}
	return ""
}
