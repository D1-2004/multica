package handler

// Official app (catalog) connectors: the workspace adds an official remote
// MCP server from connectorcatalog in one click; people connect their own
// account through OAuth (internal_connector_oauth.go) and the relay calls the
// server with that account (internal_connector_oauth_refresh.go). Tools are
// discovered with a connected account and pinned server-side: read-only
// tools by default, all tools when the admin enables writes.
//
// The helpers in this file are the building blocks of the admin API:
//   - connectorCatalogApps           GET  .../connector-catalog
//   - createCatalogConnector         POST .../connector-catalog/{slug}
//   - refreshCatalogConnectorTools   POST .../internal-connectors/{id}/tools/refresh
//   - setCatalogConnectorWriteEnabled (also reachable through the PATCH
//     update's "write_enabled")

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/connectorcatalog"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

const (
	// maxDiscoveredConnectorTools caps the discovery snapshot of a catalog
	// connector; maxPinnedConnectorTools caps allowed_tools of every
	// connector.
	maxDiscoveredConnectorTools = 256
	maxPinnedConnectorTools     = 64
	catalogDiscoveryTimeout     = 30 * time.Second
)

var (
	// connectorCatalog is the official app registry. Tests replace it.
	connectorCatalog = connectorcatalog.Default()
	// catalogExternalClient returns the proxy-aware, host-pinned client of
	// one app. Tests replace it with an httptest transport.
	catalogExternalClient = defaultCatalogExternalClient
	// catalogSessionCache keeps established MCP sessions per (connector,
	// access token hash) for 10 minutes, in process only.
	catalogSessionCache = remotemcp.NewSessionCache(10*time.Minute, 4096)

	catalogClientsMu sync.Mutex
	catalogClients   = map[string]*remotemcp.ExternalClient{}
)

// Errors of the catalog helpers. The API layer maps them to statuses.
var (
	errCatalogAppUnknown      = errors.New("unknown official app")
	errConnectorNotFound      = errors.New("connector not found")
	errConnectorNotCatalog    = errors.New("connector is not an official app connector")
	errCatalogNoCredential    = errors.New("connect an account before discovering tools")
	errCatalogDiscoveryFailed = errors.New("could not list the official app's tools with the connected account")
)

func defaultCatalogExternalClient(app connectorcatalog.App) *remotemcp.ExternalClient {
	key := app.Slug + "|" + strings.Join(app.Hosts, ",")
	catalogClientsMu.Lock()
	defer catalogClientsMu.Unlock()
	if client, ok := catalogClients[key]; ok {
		return client
	}
	client := remotemcp.NewExternalClient(app.Hosts)
	catalogClients[key] = client
	return client
}

// catalogApp returns the official app with slug.
func catalogApp(slug string) (connectorcatalog.App, bool) {
	if slug == "" {
		return connectorcatalog.App{}, false
	}
	return connectorCatalog.Lookup(slug)
}

// connectorOAuthDeploymentError reports why an OAuth connect of app cannot
// start on this deployment, as the start endpoint's *connectorOAuthError, or
// nil when it can: credentials can be sealed, the app's OAuth flow is
// configured (GitHub needs the GitHub App client id and secret) and an app
// origin is set.
func (h *Handler) connectorOAuthDeploymentError(app connectorcatalog.App) error {
	switch {
	case h.InternalConnectorSecretBox == nil:
		return oauthStartError(http.StatusServiceUnavailable, "credential_storage_unavailable", "connector credential storage is not configured")
	case !app.OAuthAvailable(githubUserAuthorizationConfigured()):
		return oauthStartError(http.StatusServiceUnavailable, connectOAuthErrOAuthNotEnabled, "OAuth is not configured for this app")
	case h.connectorOAuthAppOrigin() == "":
		return oauthStartError(http.StatusServiceUnavailable, "app_origin_missing", "no app origin is configured")
	}
	return nil
}

// catalogOAuthAvailable reports whether an OAuth connect of app can start on
// this deployment (connectorOAuthDeploymentError), so a client that shows
// 连接 only when it is true never offers a connect the start endpoint
// refuses for configuration reasons.
func (h *Handler) catalogOAuthAvailable(app connectorcatalog.App) bool {
	return h.connectorOAuthDeploymentError(app) == nil
}

// connectorURLIsCatalogTemplate reports whether raw is exactly an official
// app's MCP URL.
func connectorURLIsCatalogTemplate(raw string) bool {
	_, ok := connectorCatalog.SlugForURL(raw)
	return ok
}

// connectorAcceptsBearer reports whether a pasted token (workspace, scene or
// person) is accepted for a connector: every Bearer connector, and official
// apps that allow a Personal Access Token (GitHub).
func connectorAcceptsBearer(authMode, catalogSlug string) bool {
	if authMode == "bearer" {
		return true
	}
	if authMode != "oauth" {
		return false
	}
	app, ok := catalogApp(catalogSlug)
	return ok && app.AllowsPAT
}

// githubAppInstallURL is where people install the GitHub App on their
// repositories ("" when GITHUB_APP_SLUG is unset). GitHub App user tokens
// only see repositories where the App is installed.
func githubAppInstallURL() string {
	slug := githubAppSlug()
	if slug == "" {
		return ""
	}
	return "https://github.com/apps/" + url.PathEscape(slug) + "/installations/new"
}

// catalogAppFacts are what the connector catalog and an agent's connected
// apps both say about one official app on this deployment.
type catalogAppFacts struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	AuthKind string `json:"auth_kind"`
	// AllowsPAT: a Personal Access Token can be saved for the app on this
	// deployment (the app accepts one and credential storage is configured,
	// which the credential endpoints require).
	AllowsPAT bool `json:"allows_pat"`
	// OAuthAvailable: an OAuth connect can start (catalogOAuthAvailable).
	OAuthAvailable bool `json:"oauth_available"`
}

func (h *Handler) catalogAppFactsView(app connectorcatalog.App) catalogAppFacts {
	return catalogAppFacts{
		Slug: app.Slug, Name: app.Name, AuthKind: string(app.AuthKind),
		AllowsPAT: app.AllowsPAT && h.InternalConnectorSecretBox != nil, OAuthAvailable: h.catalogOAuthAvailable(app),
	}
}

// catalogAppView is one entry of GET .../connector-catalog.
type catalogAppView struct {
	catalogAppFacts
	MCPURL      string  `json:"mcp_url"`
	ConnectorID *string `json:"connector_id"`
}

// connectorCatalogApps lists the official apps with the workspace's
// connector id for each app already added (nil otherwise).
func (h *Handler) connectorCatalogApps(ctx context.Context, workspaceID string) ([]catalogAppView, error) {
	rows, err := h.DB.Query(ctx, `SELECT catalog_slug, id::text FROM internal_connector
		WHERE workspace_id = $1::uuid AND catalog_slug <> ''`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	added := map[string]string{}
	for rows.Next() {
		var slug, id string
		if err := rows.Scan(&slug, &id); err != nil {
			return nil, err
		}
		added[slug] = id
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []catalogAppView{}
	for _, app := range connectorCatalog.Apps() {
		view := catalogAppView{catalogAppFacts: h.catalogAppFactsFor(ctx, workspaceID, app), MCPURL: app.MCPURL}
		if id, ok := added[app.Slug]; ok {
			id := id
			view.ConnectorID = &id
		}
		out = append(out, view)
	}
	return out, nil
}

// internalConnectorCatalogSlug returns the catalog slug of a connector
// ("" for custom connectors) or pgx.ErrNoRows.
func (h *Handler) internalConnectorCatalogSlug(ctx context.Context, workspaceID, connectorID string) (string, error) {
	var slug string
	err := h.DB.QueryRow(ctx, `SELECT catalog_slug FROM internal_connector WHERE id = $1::uuid AND workspace_id = $2::uuid`, connectorID, workspaceID).Scan(&slug)
	return slug, err
}

// loadInternalConnector loads one connector of the workspace, or
// errConnectorNotFound.
func (h *Handler) loadInternalConnector(ctx context.Context, workspaceID, connectorID string) (internalConnector, error) {
	c, err := scanInternalConnector(h.DB.QueryRow(ctx, `SELECT `+internalConnectorSelect+`
		FROM internal_connector c WHERE c.id = $1::uuid AND c.workspace_id = $2::uuid`, connectorID, workspaceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return internalConnector{}, errConnectorNotFound
	}
	return c, err
}

// createCatalogConnector adds the official app slug to the workspace's
// connector library, or returns the connector that already exists for it
// (created=false). A new connector is enabled, uses auth_mode 'oauth', has
// no agent grants and no tools until an account is connected. Creation is
// serialized per (workspace, slug) with an advisory lock; the partial unique
// index internal_connector_catalog_slug_idx is the backstop. The returned
// connector is decorated like ListInternalConnectors items.
func (h *Handler) createCatalogConnector(ctx context.Context, workspaceID, slug string) (internalConnector, bool, error) {
	app, ok := catalogApp(slug)
	if !ok {
		return internalConnector{}, false, errCatalogAppUnknown
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return internalConnector{}, false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('connector_catalog:' || $1::text || ':' || $2::text, 0))`, workspaceID, slug); err != nil {
		return internalConnector{}, false, err
	}
	var existingID string
	err = tx.QueryRow(ctx, `SELECT id::text FROM internal_connector WHERE workspace_id = $1::uuid AND catalog_slug = $2`, workspaceID, slug).Scan(&existingID)
	created := false
	switch {
	case err == nil:
	case errors.Is(err, pgx.ErrNoRows):
		existingID = uuid.NewString()
		if _, err := tx.Exec(ctx, `INSERT INTO internal_connector
			(id, workspace_id, name, upstream_url, credential_ref, auth_mode, allowed_tools, enabled, catalog_slug, discovered_tools, write_enabled)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, 'oauth', '[]'::jsonb, TRUE, $6, '[]'::jsonb, FALSE)`,
			existingID, workspaceID, app.Name, app.MCPURL, connectorCredentialRef(existingID), slug); err != nil {
			return internalConnector{}, false, err
		}
		created = true
	default:
		return internalConnector{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return internalConnector{}, false, err
	}
	c, err := h.loadInternalConnector(ctx, workspaceID, existingID)
	if err != nil {
		return internalConnector{}, false, err
	}
	if err := h.decorateInternalConnectorView(ctx, &c); err != nil {
		return internalConnector{}, false, err
	}
	if created {
		slog.InfoContext(ctx, "official app connector added", "workspace_id", workspaceID, "connector_id", existingID, "catalog_slug", slug)
	}
	return c, created, nil
}

// discoveredConnectorTool is one entry of internal_connector.discovered_tools.
type discoveredConnectorTool struct {
	Name     string `json:"name"`
	ReadOnly bool   `json:"read_only"`
}

func decodeDiscoveredTools(raw []byte) []discoveredConnectorTool {
	var tools []discoveredConnectorTool
	if len(raw) == 0 || json.Unmarshal(raw, &tools) != nil {
		return nil
	}
	return tools
}

// pinnedCatalogTools computes allowed_tools of a catalog connector: its
// read-only tools, then (when writeEnabled) its other tools, each group in
// discovery order, capped at 64. Names that are empty, longer than 128 bytes
// or contain ',', CR or LF are skipped, and so is a tool whose presented
// (shortened) name collides with an earlier one.
func pinnedCatalogTools(discovered []discoveredConnectorTool, writeEnabled bool) []string {
	out := []string{}
	seen := map[string]bool{}
	presented := map[string]bool{}
	add := func(tool discoveredConnectorTool) {
		if len(out) >= maxPinnedConnectorTools || tool.Name == "" || len(tool.Name) > 128 || strings.ContainsAny(tool.Name, ",\r\n") || seen[tool.Name] {
			return
		}
		shown := connectorPresentedToolName(tool.Name)
		if presented[shown] {
			return
		}
		seen[tool.Name] = true
		presented[shown] = true
		out = append(out, tool.Name)
	}
	for _, tool := range discovered {
		if tool.ReadOnly {
			add(tool)
		}
	}
	if writeEnabled {
		for _, tool := range discovered {
			if !tool.ReadOnly {
				add(tool)
			}
		}
	}
	return out
}

// catalogSessionKey scopes a cached MCP session to one connector and one
// access token, so a refreshed or different account never reuses it.
func catalogSessionKey(connectorID, token string) string {
	sum := sha256.Sum256([]byte(token))
	return connectorID + ":" + hex.EncodeToString(sum[:16])
}

func bearerHeader(token string) http.Header {
	return http.Header{"Authorization": []string{"Bearer " + token}}
}

// catalogConnectorMCP returns the session-aware client of a catalog
// connector.
func catalogConnectorMCP(c internalConnector) (*remotemcp.MCPClient, connectorcatalog.App, error) {
	app, ok := catalogApp(c.CatalogSlug)
	if !ok {
		return nil, app, errCatalogAppUnknown
	}
	if c.UpstreamURL != app.MCPURL {
		return nil, app, errors.New("official app connector URL does not match the catalog")
	}
	client, err := catalogExternalClient(app).MCP(app.MCPURL, catalogSessionCache)
	return client, app, err
}

// catalogToolRefresh is the result of a tool discovery.
type catalogToolRefresh struct {
	Discovered   int      `json:"discovered"`
	AllowedTools []string `json:"allowed_tools"`
}

// discoverCatalogConnectorTools lists the tools of catalog connector c with
// its current credential (tools/list only; nothing is invoked), refreshing
// an expiring OAuth token first and once more after a 401, and stores
// discovered_tools and the recomputed allowed_tools. A failure of this one
// credential is errConnectorReconnectRequired (the account must be
// connected again) or errCatalogDiscoveryFailed (anything else: a rejected
// PAT, a failed refresh, an upstream error), so callers can move on to
// another credential.
func (h *Handler) discoverCatalogConnectorTools(ctx context.Context, c *internalConnector) (catalogToolRefresh, error) {
	if c.CatalogSlug == "" {
		return catalogToolRefresh{}, errConnectorNotCatalog
	}
	mcp, _, err := catalogConnectorMCP(*c)
	if err != nil {
		return catalogToolRefresh{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, catalogDiscoveryTimeout)
	defer cancel()
	credentialFailure := func(err error) error {
		if errors.Is(err, errConnectorReconnectRequired) {
			return err
		}
		slog.WarnContext(ctx, "official app tool discovery could not use the credential", "connector_id", c.ID, "catalog_slug", c.CatalogSlug,
			"credential_layer", c.credentialLayer, "failure_class", connectorTestFailureMessage(catalogUpstreamError(err)))
		return errCatalogDiscoveryFailed
	}
	token, err := h.freshConnectorToken(ctx, c, "")
	if err != nil {
		return catalogToolRefresh{}, credentialFailure(err)
	}
	listed, truncated, err := mcp.ListTools(ctx, catalogSessionKey(c.ID, token), bearerHeader(token), maxDiscoveredConnectorTools)
	var status *remotemcp.StatusError
	if errors.As(err, &status) && status.StatusCode == http.StatusUnauthorized {
		next, refreshErr := h.freshConnectorToken(ctx, c, token)
		if refreshErr != nil {
			return catalogToolRefresh{}, credentialFailure(refreshErr)
		}
		listed, truncated, err = mcp.ListTools(ctx, catalogSessionKey(c.ID, next), bearerHeader(next), maxDiscoveredConnectorTools)
	}
	if err != nil {
		slog.WarnContext(ctx, "official app tool discovery failed", "connector_id", c.ID, "catalog_slug", c.CatalogSlug, "failure_class", connectorTestFailureMessage(catalogUpstreamError(err)))
		return catalogToolRefresh{}, errCatalogDiscoveryFailed
	}
	if truncated {
		slog.InfoContext(ctx, "official app tool discovery truncated", "connector_id", c.ID, "catalog_slug", c.CatalogSlug, "kept", len(listed))
	}
	discovered := make([]discoveredConnectorTool, 0, len(listed))
	for _, tool := range listed {
		discovered = append(discovered, discoveredConnectorTool{Name: tool.Name, ReadOnly: tool.ReadOnly})
	}
	return h.storeCatalogConnectorTools(ctx, c, discovered)
}

// storeCatalogConnectorTools writes a discovery snapshot and recomputes
// allowed_tools under the connector's row lock, so a concurrent write toggle
// is not lost.
func (h *Handler) storeCatalogConnectorTools(ctx context.Context, c *internalConnector, discovered []discoveredConnectorTool) (catalogToolRefresh, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return catalogToolRefresh{}, err
	}
	defer tx.Rollback(ctx)
	var writeEnabled bool
	if err := tx.QueryRow(ctx, `SELECT write_enabled FROM internal_connector WHERE id = $1::uuid AND workspace_id = $2::uuid AND catalog_slug = $3 FOR UPDATE`,
		c.ID, c.WorkspaceID, c.CatalogSlug).Scan(&writeEnabled); errors.Is(err, pgx.ErrNoRows) {
		return catalogToolRefresh{}, errConnectorNotFound
	} else if err != nil {
		return catalogToolRefresh{}, err
	}
	allowed := pinnedCatalogTools(discovered, writeEnabled)
	discoveredRaw, err := json.Marshal(discovered)
	if err != nil {
		return catalogToolRefresh{}, err
	}
	allowedRaw, err := json.Marshal(allowed)
	if err != nil {
		return catalogToolRefresh{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE internal_connector SET discovered_tools = $3, allowed_tools = $4, updated_at = now()
		WHERE id = $1::uuid AND workspace_id = $2::uuid`, c.ID, c.WorkspaceID, discoveredRaw, allowedRaw); err != nil {
		return catalogToolRefresh{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return catalogToolRefresh{}, err
	}
	c.DiscoveredTools, c.DiscoveredToolCount, c.AllowedTools, c.WriteEnabled = discovered, len(discovered), allowed, writeEnabled
	slog.InfoContext(ctx, "official app tools discovered", "connector_id", c.ID, "catalog_slug", c.CatalogSlug, "discovered", len(discovered), "pinned", len(allowed))
	return catalogToolRefresh{Discovered: len(discovered), AllowedTools: allowed}, nil
}

// discoverCatalogToolsIfEmpty runs the first discovery of a catalog
// connector with a credential that was just stored (OAuth callback, or a
// pasted PAT). It does nothing when tools were already discovered or the
// connector is not a catalog connector. Failures are logged and returned;
// callers treat them as non-fatal (the admin can refresh tools later).
// secret is the stored credential; key locates a scene or person credential
// (zero for the workspace credential).
func (h *Handler) discoverCatalogToolsIfEmpty(ctx context.Context, workspaceID, connectorID string, secret contextcap.Secret, layer string, key contextcap.CredentialBinding) (catalogToolRefresh, bool, error) {
	c, err := h.loadInternalConnector(ctx, workspaceID, connectorID)
	if err != nil {
		return catalogToolRefresh{}, false, err
	}
	if c.CatalogSlug == "" || len(c.DiscoveredTools) > 0 {
		return catalogToolRefresh{Discovered: len(c.DiscoveredTools), AllowedTools: c.AllowedTools}, false, nil
	}
	c.setResolvedSecret(secret, layer, key)
	result, err := h.discoverCatalogConnectorTools(ctx, &c)
	return result, err == nil, err
}

// refreshCatalogConnectorTools re-discovers the tools of a catalog
// connector (the admin "refresh tools" action). It uses the workspace
// credential when one is usable, else the most recently updated usable scene
// or person credential of the connector, because tool lists do not depend on
// which account lists them for the official apps in the catalog. A
// credential that cannot list the tools (revoked, a rejected PAT, a failed
// refresh) is skipped for the next one. errCatalogNoCredential when no
// usable account is connected anywhere; errCatalogDiscoveryFailed when
// accounts exist but none could list the tools.
func (h *Handler) refreshCatalogConnectorTools(ctx context.Context, workspaceID, connectorID string) (catalogToolRefresh, error) {
	c, err := h.loadInternalConnector(ctx, workspaceID, connectorID)
	if err != nil {
		return catalogToolRefresh{}, err
	}
	if c.CatalogSlug == "" {
		return catalogToolRefresh{}, errConnectorNotCatalog
	}
	now := time.Now()
	failed := errCatalogNoCredential
	// skip reports whether err is a failure of one credential only.
	skip := func(err error) bool {
		if errors.Is(err, errCatalogDiscoveryFailed) {
			failed = errCatalogDiscoveryFailed
			return true
		}
		return errors.Is(err, errConnectorReconnectRequired)
	}
	if secret, err := h.connectorSecret(c); err == nil && secret.Usable(now) {
		workspace := c
		workspace.setResolvedSecret(secret, connectorCredentialWorkspace, contextcap.CredentialBinding{})
		result, err := h.discoverCatalogConnectorTools(ctx, &workspace)
		if !skip(err) {
			return result, err
		}
	}
	box := h.contextCredentialBox()
	if box == nil {
		return catalogToolRefresh{}, failed
	}
	credentials, err := contextcap.ConnectorCredentials(ctx, h.DB, workspaceID, connectorID, 10)
	if err != nil {
		return catalogToolRefresh{}, err
	}
	for _, credential := range credentials {
		key := credential.CredentialBinding
		secret, err := contextcap.OpenCredentialSecret(box, key, credential.Ciphertext)
		if err != nil || !secret.Usable(now) {
			continue
		}
		c.setResolvedSecret(secret, key.ScopeType, key)
		result, err := h.discoverCatalogConnectorTools(ctx, &c)
		if skip(err) {
			continue
		}
		return result, err
	}
	return catalogToolRefresh{}, failed
}

// setCatalogConnectorWriteEnabled turns write tools of a catalog connector
// on or off and recomputes allowed_tools from its discovered tools in one
// transaction. It returns the new allowed_tools.
func (h *Handler) setCatalogConnectorWriteEnabled(ctx context.Context, workspaceID, connectorID string, enabled bool) ([]string, error) {
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var slug string
	var discoveredRaw []byte
	err = tx.QueryRow(ctx, `SELECT catalog_slug, discovered_tools FROM internal_connector WHERE id = $1::uuid AND workspace_id = $2::uuid FOR UPDATE`,
		connectorID, workspaceID).Scan(&slug, &discoveredRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errConnectorNotFound
	}
	if err != nil {
		return nil, err
	}
	if slug == "" {
		return nil, errConnectorNotCatalog
	}
	allowed := pinnedCatalogTools(decodeDiscoveredTools(discoveredRaw), enabled)
	allowedRaw, err := json.Marshal(allowed)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE internal_connector SET write_enabled = $3, allowed_tools = $4, updated_at = now()
		WHERE id = $1::uuid AND workspace_id = $2::uuid`, connectorID, workspaceID, enabled, allowedRaw); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return allowed, nil
}

// catalogUpstreamError maps remotemcp errors to the relay's error classes.
func catalogUpstreamError(err error) error {
	var status *remotemcp.StatusError
	if errors.As(err, &status) {
		return connectorUpstreamStatusError{Code: status.StatusCode}
	}
	var rpcErr *remotemcp.RPCError
	if errors.As(err, &rpcErr) || errors.Is(err, remotemcp.ErrInvalidResponse) {
		return connectorUpstreamProtocolError{}
	}
	return err
}

// connectorReconnectMessage is the tool error an agent sees when the
// account behind a connector credential must be connected again.
func connectorReconnectMessage(c internalConnector) string {
	name := c.Name
	if app, ok := catalogApp(c.CatalogSlug); ok {
		name = app.Name
	}
	return fmt.Sprintf("The %s connection used for this request has expired or was revoked. Ask the user to reconnect %s in the connector settings (连接器设置) and try again.", name, name)
}
