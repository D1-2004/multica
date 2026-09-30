package handler

// OAuth token freshness for official app connectors. A credential is sealed
// in one row (internal_connector.credential_ciphertext for the workspace
// layer, context_connector_credential for scene and person layers); a
// refresh locks that row (SELECT ... FOR UPDATE in its own transaction),
// re-opens it, refreshes only if it is still stale, reseals and commits, so
// concurrent relay calls on any replica refresh a token exactly once and the
// others pick up the new token. A refresh token the provider rejects
// (invalid_grant) deletes the credential: the connection is lost and the UI
// shows it as not connected.
//
// The locked section is detached from the caller's request: providers that
// rotate refresh tokens (GitHub ghr_, most DCR apps) invalidate the old one
// as soon as they answer, so an answer must be stored even when the relay
// request that asked for it is cancelled meanwhile. A token is always
// refreshed with the OAuth client that issued it (OAuthToken.ClientID), so a
// replaced dynamic registration never turns into a spurious invalid_grant.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/connectorcatalog"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

const (
	// connectorOAuthRefreshSkew refreshes a token that expires this soon.
	connectorOAuthRefreshSkew = 60 * time.Second
	connectorOAuthHTTPTimeout = 20 * time.Second
	// connectorOAuthRefreshTimeout bounds one locked refresh: the row lock
	// wait, the token request, the reseal and the commit.
	connectorOAuthRefreshTimeout = connectorOAuthHTTPTimeout + 10*time.Second
)

var (
	// errConnectorReconnectRequired means the account behind a credential
	// must be connected again (revoked grant, expired token without a
	// refresh token, or the credential was removed).
	errConnectorReconnectRequired = errors.New("connector account must be reconnected")
	// errConnectorOAuthClientReplaced means the OAuth client that issued a
	// token is no longer known (the GitHub App changed, or the dynamic
	// registration was replaced beyond the kept history), so the token
	// cannot be exchanged or refreshed.
	errConnectorOAuthClientReplaced = errors.New("the OAuth client that issued this token is no longer configured")
)

// callCatalogConnector sends one JSON-RPC request of a relay call to an
// official app connector. The token is refreshed first when it is about to
// expire; an upstream 401 triggers one forced refresh and one retry (a 401
// proves the request did not run, so this is safe for tools/call too).
func (h *Handler) callCatalogConnector(ctx context.Context, c *internalConnector, method string, params map[string]any) (json.RawMessage, error) {
	mcp, _, err := catalogConnectorMCP(*c)
	if err != nil {
		return nil, err
	}
	token, err := h.freshConnectorToken(ctx, c, "")
	if err != nil {
		return nil, err
	}
	result, err := mcp.Call(ctx, catalogSessionKey(c.ID, token), bearerHeader(token), method, params)
	var status *remotemcp.StatusError
	if errors.As(err, &status) && status.StatusCode == http.StatusUnauthorized {
		next, refreshErr := h.freshConnectorToken(ctx, c, token)
		if refreshErr != nil {
			return nil, refreshErr
		}
		result, err = mcp.Call(ctx, catalogSessionKey(c.ID, next), bearerHeader(next), method, params)
	}
	if err != nil {
		return nil, catalogUpstreamError(err)
	}
	return result, nil
}

// freshConnectorToken returns the Bearer to send for c. rejected is the
// token upstream just answered 401 for ("" when none): it forces a refresh
// unless another caller already replaced that token. A non-OAuth credential
// is returned as is (a rejected one yields an upstream 401 error). The
// refreshed secret is pinned on c.
func (h *Handler) freshConnectorToken(ctx context.Context, c *internalConnector, rejected string) (string, error) {
	secret, err := h.connectorSecret(*c)
	if err != nil {
		return "", err
	}
	if secret.OAuth == nil {
		if rejected != "" {
			return "", connectorUpstreamStatusError{Code: http.StatusUnauthorized}
		}
		return secret.Bearer, nil
	}
	now := time.Now()
	if rejected == "" && !secret.OAuth.ExpiresWithin(now, connectorOAuthRefreshSkew) {
		return secret.Bearer, nil
	}
	if !secret.OAuth.Refreshable() {
		if rejected == "" && !secret.OAuth.ExpiresWithin(now, 0) {
			return secret.Bearer, nil
		}
		return "", errConnectorReconnectRequired
	}
	used := secret.Bearer
	if rejected != "" {
		used = rejected
	}
	fresh, err := h.refreshConnectorCredential(ctx, c, used, rejected != "")
	if err != nil {
		if !errors.Is(err, errConnectorReconnectRequired) && rejected == "" && !secret.OAuth.ExpiresWithin(now, 0) {
			// A transient refresh failure must not break a call while the
			// current token is still valid.
			slog.WarnContext(ctx, "official app token refresh failed; using the current token", "connector_id", c.ID, "credential_layer", c.credentialLayer, "error", err)
			return secret.Bearer, nil
		}
		return "", err
	}
	layer := c.credentialLayer
	if !c.bearerResolved {
		layer = connectorCredentialWorkspace
	}
	c.setResolvedSecret(fresh, layer, c.credentialKey)
	return fresh.Bearer, nil
}

// refreshConnectorCredential refreshes the stored OAuth credential behind c
// under its row lock. usedToken is the access token the caller holds; when
// the stored token differs, another caller already refreshed (or the user
// reconnected) and the stored secret is returned without a token request.
// force refreshes even a token that is not near expiry (after a 401). It
// runs detached from ctx's cancellation (see the file comment), bounded by
// connectorOAuthRefreshTimeout.
func (h *Handler) refreshConnectorCredential(ctx context.Context, c *internalConnector, usedToken string, force bool) (contextcap.Secret, error) {
	layer := c.credentialLayer
	if !c.bearerResolved {
		layer = connectorCredentialWorkspace
	}
	if h.TxStarter == nil {
		return contextcap.Secret{}, errors.New("connector store unavailable")
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), connectorOAuthRefreshTimeout)
	defer cancel()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return contextcap.Secret{}, err
	}
	defer tx.Rollback(ctx)

	var current contextcap.Secret
	switch layer {
	case connectorCredentialWorkspace:
		var ciphertext []byte
		err := tx.QueryRow(ctx, `SELECT credential_ciphertext FROM internal_connector WHERE id = $1::uuid AND workspace_id = $2::uuid FOR UPDATE`,
			c.ID, c.WorkspaceID).Scan(&ciphertext)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && len(ciphertext) == 0) {
			return contextcap.Secret{}, errConnectorReconnectRequired
		}
		if err != nil {
			return contextcap.Secret{}, err
		}
		if current, err = h.openWorkspaceConnectorSecret(c.WorkspaceID, c.ID, ciphertext); err != nil {
			return contextcap.Secret{}, err
		}
	case connectorCredentialScene, connectorCredentialPerson:
		credential, err := contextcap.LockCredential(ctx, tx, c.credentialKey)
		if errors.Is(err, contextcap.ErrNotFound) {
			return contextcap.Secret{}, errConnectorReconnectRequired
		}
		if err != nil {
			return contextcap.Secret{}, err
		}
		if current, err = contextcap.OpenCredentialSecret(h.contextCredentialBox(), c.credentialKey, credential.Ciphertext); err != nil {
			return contextcap.Secret{}, err
		}
	default:
		return contextcap.Secret{}, errors.New("connector credential layer cannot be refreshed")
	}

	if current.Bearer != usedToken {
		return current, nil
	}
	if current.OAuth == nil {
		if force {
			return contextcap.Secret{}, connectorUpstreamStatusError{Code: http.StatusUnauthorized}
		}
		return current, nil
	}
	now := time.Now()
	if !force && !current.OAuth.ExpiresWithin(now, connectorOAuthRefreshSkew) {
		return current, nil
	}
	if !current.OAuth.Refreshable() {
		return contextcap.Secret{}, errConnectorReconnectRequired
	}
	endpoint, err := h.connectorTokenEndpoint(ctx, *c, current.OAuth.ClientID)
	if errors.Is(err, errConnectorOAuthClientReplaced) {
		// Not a revocation: the credential stays, but it cannot be renewed.
		slog.WarnContext(ctx, "official app token issued by a replaced OAuth client; reconnect required", "connector_id", c.ID, "credential_layer", layer)
		return contextcap.Secret{}, errConnectorReconnectRequired
	}
	if err != nil {
		return contextcap.Secret{}, err
	}
	refreshCtx, cancelRefresh := context.WithTimeout(ctx, connectorOAuthHTTPTimeout)
	defer cancelRefresh()
	response, err := endpoint.client.RefreshOAuthToken(refreshCtx, endpoint.tokenURL, endpoint.resource, current.OAuth.RefreshToken, endpoint.registration)
	if remotemcp.IsInvalidGrant(err) {
		if deleteErr := h.deleteConnectorCredentialLocked(ctx, tx, *c, layer); deleteErr != nil {
			return contextcap.Secret{}, deleteErr
		}
		if err := tx.Commit(ctx); err != nil {
			return contextcap.Secret{}, err
		}
		slog.WarnContext(ctx, "official app refresh token rejected; credential removed", "connector_id", c.ID, "credential_layer", layer)
		return contextcap.Secret{}, errConnectorReconnectRequired
	}
	if err != nil {
		return contextcap.Secret{}, err
	}
	next := *current.OAuth
	next.AccessToken = response.AccessToken
	next.ClientID = endpoint.registration.ClientID
	if response.RefreshToken != "" {
		next.RefreshToken = response.RefreshToken
	}
	next.ExpiresAt = 0
	if expiry := remotemcp.OAuthExpiry(now, response.ExpiresIn); !expiry.IsZero() {
		next.ExpiresAt = expiry.Unix()
	}
	if response.Scope != "" {
		next.Scope = response.Scope
	}
	fresh := contextcap.Secret{Bearer: next.AccessToken, OAuth: &next}
	if err := h.replaceConnectorCredentialLocked(ctx, tx, *c, layer, fresh); err != nil {
		return contextcap.Secret{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return contextcap.Secret{}, err
	}
	slog.InfoContext(ctx, "official app token refreshed", "connector_id", c.ID, "credential_layer", layer)
	return fresh, nil
}

func (h *Handler) replaceConnectorCredentialLocked(ctx context.Context, tx pgx.Tx, c internalConnector, layer string, secret contextcap.Secret) error {
	if layer == connectorCredentialWorkspace {
		sealed, err := h.sealWorkspaceConnectorSecret(c.WorkspaceID, c.ID, secret)
		if err != nil {
			return err
		}
		// updated_at is left alone: a token refresh is not an admin change.
		_, err = tx.Exec(ctx, `UPDATE internal_connector SET credential_ciphertext = $3 WHERE id = $1::uuid AND workspace_id = $2::uuid`, c.ID, c.WorkspaceID, sealed)
		return err
	}
	sealed, err := contextcap.SealOAuthCredential(h.contextCredentialBox(), c.credentialKey, *secret.OAuth)
	if err != nil {
		return err
	}
	_, err = contextcap.ReplaceCredentialSecret(ctx, tx, c.credentialKey, sealed, contextcap.OAuthHint(secret.OAuth.Account))
	return err
}

func (h *Handler) deleteConnectorCredentialLocked(ctx context.Context, tx pgx.Tx, c internalConnector, layer string) error {
	if layer == connectorCredentialWorkspace {
		_, err := tx.Exec(ctx, `UPDATE internal_connector SET credential_ciphertext = NULL, updated_at = now() WHERE id = $1::uuid AND workspace_id = $2::uuid`, c.ID, c.WorkspaceID)
		return err
	}
	_, err := contextcap.DeleteCredential(ctx, tx, c.credentialKey)
	return err
}

// connectorTokenEndpointConfig is where and how a connector's tokens are
// exchanged and refreshed.
type connectorTokenEndpointConfig struct {
	client       *remotemcp.ExternalClient
	tokenURL     string
	resource     string
	redirectURI  string
	registration remotemcp.OAuthClientRegistration
}

// connectorTokenEndpoint returns the token endpoint of a catalog connector
// for the OAuth client clientID ("" = the current one): the deployment's
// GitHub App client for GitHub, the connector's dynamic registration (the
// current one or a kept earlier one) for DCR apps.
// errConnectorOAuthClientReplaced when clientID is no longer known.
func (h *Handler) connectorTokenEndpoint(ctx context.Context, c internalConnector, clientID string) (connectorTokenEndpointConfig, error) {
	app, ok := catalogApp(c.CatalogSlug)
	if !ok {
		return connectorTokenEndpointConfig{}, errConnectorNotCatalog
	}
	out := connectorTokenEndpointConfig{client: catalogExternalClient(app)}
	switch app.AuthKind {
	case connectorcatalog.AuthOAuthGitHubApp:
		if !githubUserAuthorizationConfigured() {
			return connectorTokenEndpointConfig{}, errors.New("GitHub App client credentials are not configured")
		}
		appClientID := strings.TrimSpace(os.Getenv("GITHUB_APP_CLIENT_ID"))
		if clientID != "" && clientID != appClientID {
			return connectorTokenEndpointConfig{}, errConnectorOAuthClientReplaced
		}
		out.tokenURL = app.TokenEndpoint
		out.redirectURI = h.githubFrontend() + connectorOAuthGitHubCallback
		out.registration = remotemcp.OAuthClientRegistration{
			ClientID:                appClientID,
			ClientSecret:            os.Getenv("GITHUB_APP_CLIENT_SECRET"),
			TokenEndpointAuthMethod: "client_secret_post",
		}
		return out, nil
	case connectorcatalog.AuthOAuthDCR:
		record, err := h.loadConnectorOAuthClient(ctx, c.WorkspaceID, c.ID)
		if err != nil {
			return connectorTokenEndpointConfig{}, err
		}
		registration, ok := record.registrationFor(clientID)
		if !ok {
			return connectorTokenEndpointConfig{}, errConnectorOAuthClientReplaced
		}
		out.tokenURL, out.resource, out.redirectURI, out.registration = registration.TokenEndpoint, registration.Resource, registration.RedirectURI, registration.Registration
		return out, nil
	default:
		return connectorTokenEndpointConfig{}, errConnectorNotCatalog
	}
}
