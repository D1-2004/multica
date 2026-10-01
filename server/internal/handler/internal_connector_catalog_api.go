package handler

// HTTP surface of official app (catalog) connectors
// (docs/internal-mcp-connectors.md "Official apps"). The building blocks live
// in internal_connector_catalog.go (catalog, discovery, write toggle) and
// internal_connector_oauth.go (OAuth start and callback); this file maps them
// to routes, request bodies and status codes.
//
// Admin (workspace owner/admin, RequireWorkspaceMCPHumanIssuer):
//   - GET  /api/workspaces/{id}/connector-catalog
//   - POST /api/workspaces/{id}/connector-catalog/{slug}
//   - POST /api/workspaces/{id}/internal-connectors/{connectorId}/oauth/start
//   - POST /api/workspaces/{id}/internal-connectors/{connectorId}/tools/refresh
//
// Mobile (RequireDingTalkHumanActor, context capability grants):
//   - POST /api/context-capabilities/agents/{agentId}/connections/start
//
// Public provider callbacks (no session; the hashed single-use state plus the
// browser binding cookie set by the start response are the proof, and the
// router rate-limits the routes):
//   - GET /api/connectors/oauth/callback (DCR apps; path registered in
//     provider consoles)
//   - GET /api/connector-oauth/callback (v0 alias of the same handler)
//   - GET /api/github/authorize with a "mcpc." state (GitHub App), delegated
//     from GitHubAuthorizeCallback

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/connectorcatalog"
	"github.com/multica-ai/multica/server/internal/util"
)

const (
	// connectorOAuthStartBodyLimit bounds the optional start body.
	connectorOAuthStartBodyLimit = 8 << 10
	// connectorOAuthCallbackTimeout bounds one callback: code exchange,
	// account lookup and the first tool discovery.
	connectorOAuthCallbackTimeout = 90 * time.Second
	// connectorOAuthMaxCode bounds the provider's authorization code.
	connectorOAuthMaxCode = 4096
)

// catalogAppResponse is one app of GET .../connector-catalog.
type catalogAppResponse struct {
	catalogAppView
	// InstallURL is where people grant the app access to their resources
	// (GitHub App installation). Omitted when the app has none or it is not
	// configured.
	InstallURL string `json:"install_url,omitempty"`
}

// catalogAppInstallURL returns the resource installation page of an app
// ("" when it has none).
func catalogAppInstallURL(app connectorcatalog.App) string {
	if app.AuthKind != connectorcatalog.AuthOAuthGitHubApp {
		return ""
	}
	return githubAppInstallURL()
}

// catalogWorkspaceID parses the {id} route param into a canonical UUID.
func catalogWorkspaceID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return "", false
	}
	return uuidToString(id), true
}

// catalogConnectorID parses the {connectorId} route param into a canonical
// UUID.
func catalogConnectorID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "connectorId"), "connector id")
	if !ok {
		return "", false
	}
	return uuidToString(id), true
}

// ListConnectorCatalog lists the official apps with the workspace's
// connector id for each app already added.
func (h *Handler) ListConnectorCatalog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ws, ok := catalogWorkspaceID(w, r)
	if !ok {
		return
	}
	views, err := h.connectorCatalogApps(r.Context(), ws)
	if err != nil {
		slog.ErrorContext(r.Context(), "connector catalog list failed", "workspace_id", ws, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list official apps")
		return
	}
	apps := make([]catalogAppResponse, 0, len(views))
	for _, view := range views {
		item := catalogAppResponse{catalogAppView: view}
		if app, ok := catalogApp(view.Slug); ok {
			item.InstallURL = catalogAppInstallURL(app)
		}
		apps = append(apps, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": apps})
}

// AddCatalogConnector adds an official app to the workspace connector
// library (201), or returns the connector that already exists for it (200).
func (h *Handler) AddCatalogConnector(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ws, ok := catalogWorkspaceID(w, r)
	if !ok {
		return
	}
	slug := chi.URLParam(r, "slug")
	c, created, err := h.createCatalogConnector(r.Context(), ws, slug)
	if errors.Is(err, errCatalogAppUnknown) {
		writeError(w, http.StatusNotFound, "official app not found")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "official app add failed", "workspace_id", ws, "catalog_slug", slug, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to add the official app")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		slog.InfoContext(r.Context(), "official app added by admin", "workspace_id", ws, "connector_id", c.ID, "catalog_slug", slug, "actor_id", requestUserID(r))
	}
	writeJSON(w, status, map[string]any{"connector": c})
}

// StartInternalConnectorOAuth starts connecting the workspace-wide shared
// account of an official app connector. Body: optional {"return_to"}; unknown
// fields are rejected. The response binds the connect to the browser that
// receives it (see "Browser binding" in internal_connector_oauth.go), so the
// desktop app does not call it and sends the admin to the web page instead.
func (h *Handler) StartInternalConnectorOAuth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ws, ok := catalogWorkspaceID(w, r)
	if !ok {
		return
	}
	connectorID, ok := catalogConnectorID(w, r)
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var input struct {
		ReturnTo string `json:"return_to"`
	}
	if !decodeOptionalJSONBody(w, r, connectorOAuthStartBodyLimit, &input) {
		return
	}
	started, err := h.startConnectorOAuth(r.Context(), connectorOAuthStart{
		connectorOAuthScope: connectorOAuthScope{
			WorkspaceID: ws, ConnectorID: connectorID, UserID: userID, ScopeType: connectorOAuthScopeWorkspace,
		},
		ReturnTo: input.ReturnTo,
	})
	if err != nil {
		writeConnectorOAuthStartError(w, r, err, false)
		return
	}
	writeConnectorOAuthStarted(w, started)
}

// writeConnectorOAuthStarted answers a start with {"authorize_url"} and sets
// the state's browser binding cookies (canonical callback path, plus the
// console-registered alias when the connect uses it).
func writeConnectorOAuthStarted(w http.ResponseWriter, started connectorOAuthStarted) {
	http.SetCookie(w, started.Cookie)
	for _, cookie := range started.ExtraCookies {
		http.SetCookie(w, cookie)
	}
	writeJSON(w, http.StatusOK, map[string]string{"authorize_url": started.AuthorizeURL})
}

// RefreshInternalConnectorTools re-discovers an official app connector's
// tools with a connected account and re-pins them.
func (h *Handler) RefreshInternalConnectorTools(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ws, ok := catalogWorkspaceID(w, r)
	if !ok {
		return
	}
	connectorID, ok := catalogConnectorID(w, r)
	if !ok {
		return
	}
	result, err := h.refreshCatalogConnectorTools(r.Context(), ws, connectorID)
	switch {
	case err == nil:
	case errors.Is(err, errConnectorNotFound):
		writeError(w, http.StatusNotFound, "connector not found")
		return
	case errors.Is(err, errConnectorNotCatalog):
		writeErrorCode(w, http.StatusBadRequest, "not_official_app", "tools of custom connectors are pinned when they are created")
		return
	case errors.Is(err, errCatalogAppUnknown):
		writeErrorCode(w, http.StatusConflict, "official_app_unavailable", "this official app is no longer in the catalog")
		return
	case errors.Is(err, errCatalogNoCredential), errors.Is(err, errConnectorReconnectRequired):
		writeErrorCode(w, http.StatusConflict, "no_connected_account", "connect an account before refreshing tools")
		return
	case errors.Is(err, errCatalogDiscoveryFailed):
		writeErrorCode(w, http.StatusBadGateway, "discovery_failed", "could not list the official app's tools with the connected account")
		return
	default:
		slog.ErrorContext(r.Context(), "official app tool refresh failed", "workspace_id", ws, "connector_id", connectorID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to refresh tools")
		return
	}
	if result.AllowedTools == nil {
		result.AllowedTools = []string{}
	}
	slog.InfoContext(r.Context(), "official app tools refreshed by admin", "workspace_id", ws, "connector_id", connectorID,
		"discovered", result.Discovered, "pinned", len(result.AllowedTools), "actor_id", requestUserID(r))
	writeJSON(w, http.StatusOK, result)
}

// StartContextConfigConnection starts connecting the caller's own account
// (person scope) or a group's account (scene scope) of an official app
// connector from the mobile configuration page. It requires the caller's
// live grant for exactly that scope, or for a group scene of an agent the
// caller manages (contextCapRequireScope). A 1:1 chat scene connects its
// person's account, which only that person may do (403 person_only for a
// manager).
// startConnectorOAuth then applies the PUT credentials connector rule
// (offered or globally granted) and re-checks it at
// the callback.
func (h *Handler) StartContextConfigConnection(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.contextCapMobileUser(w, r)
	if !ok {
		return
	}
	a, ok := h.contextCapAgentOr404(w, r)
	if !ok {
		return
	}
	var input struct {
		ScopeType   string `json:"scope_type"`
		ScopeKey    string `json:"scope_key"`
		OrgID       string `json:"org_id"`
		ConnectorID string `json:"connector_id"`
		ReturnTo    string `json:"return_to"`
	}
	if !decodeContextCapBody(w, r, contextCapBodyLimit, &input) {
		return
	}
	if a, ok = h.contextCapRequestOrg(w, r, a, userID, contextCapScopeOrg(input.ScopeType, input.ScopeKey, input.OrgID)); !ok {
		return
	}
	connectorUUID, err := util.ParseUUID(strings.TrimSpace(input.ConnectorID))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid connector_id")
		return
	}
	grant, ok := h.contextCapRequireScope(w, r, a, userID, input.ScopeType, input.ScopeKey, contextCapNeedCredential)
	if !ok {
		return
	}
	started, err := h.startConnectorOAuth(r.Context(), connectorOAuthStart{
		connectorOAuthScope: connectorOAuthScope{
			WorkspaceID: a.WorkspaceID, ConnectorID: uuidToString(connectorUUID), UserID: userID,
			ScopeType: grant.ScopeType, AgentID: a.ID, OrgID: a.OrgID, ScopeKey: grant.ScopeKey,
		},
		ReturnTo: input.ReturnTo,
	})
	if err != nil {
		writeConnectorOAuthStartError(w, r, err, true)
		return
	}
	writeConnectorOAuthStarted(w, started)
}

func init() {
	connectorOAuthCompleteLocal = (*Handler).completeConnectorOAuthCallback
}

// completeConnectorOAuthCallback completes a connect of this deployment
// from a provider redirect (serveConnectorOAuthCallback has already
// forwarded other deployments' callbacks) and sends the browser to the
// destination recorded at start with
// ?connected=<slug> or ?connect_error=<code>. It reads and clears the
// state's browser binding cookie. An unknown, expired or replayed state has
// no trusted destination and gets a small page linking back to the app
// instead. The work runs detached from the browser request (bounded by its
// own timeout), because the state is consumed first and a dropped
// connection must not lose a code that was already accepted.
func (h *Handler) completeConnectorOAuthCallback(w http.ResponseWriter, r *http.Request, via string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	query := r.URL.Query()
	callback := connectorOAuthCallback{Via: via, State: query.Get("state"), Code: query.Get("code"), Error: query.Get("error")}
	if len(callback.Code) > connectorOAuthMaxCode {
		// Treated like a provider error: the state is still consumed.
		callback.Code, callback.Error = "", "invalid_request"
	}
	if validConnectorOAuthState(callback.State) {
		stateHash := hashConnectorOAuthState(callback.State)
		if cookie, err := r.Cookie(connectorOAuthCookieName(stateHash)); err == nil {
			callback.BrowserNonce = cookie.Value
			origin, path := h.connectorOAuthCallbackTarget(via)
			http.SetCookie(w, connectorOAuthBrowserCookie(stateHash, "", origin, path))
			if via == connectorOAuthViaDCR && path != connectorOAuthCallbackLegacyPath {
				http.SetCookie(w, connectorOAuthBrowserCookie(stateHash, "", origin, connectorOAuthCallbackLegacyPath))
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), connectorOAuthCallbackTimeout)
	defer cancel()
	outcome := h.completeConnectorOAuth(ctx, callback)
	if outcome.RedirectURL == "" {
		h.writeConnectorOAuthInvalidPage(w)
		return
	}
	http.Redirect(w, r, outcome.RedirectURL, http.StatusFound)
}

// writeConnectorOAuthStartError maps a startConnectorOAuth error to a
// response. mobile answers a missing connector like an unavailable one
// (403), as PUT credentials does.
func writeConnectorOAuthStartError(w http.ResponseWriter, r *http.Request, err error, mobile bool) {
	var oauthErr *connectorOAuthError
	if !errors.As(err, &oauthErr) {
		slog.ErrorContext(r.Context(), "official app OAuth start failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not start the connection")
		return
	}
	if mobile && oauthErr.Status == http.StatusNotFound {
		writeErrorCode(w, http.StatusForbidden, connectOAuthErrForbidden, "this connector is not available for the agent")
		return
	}
	writeErrorCode(w, oauthErr.Status, oauthErr.Code, oauthErr.Message)
}

// decodeOptionalJSONBody decodes a bounded JSON object with no unknown
// fields and no trailing data; an empty body leaves dst unchanged.
func decodeOptionalJSONBody(w http.ResponseWriter, r *http.Request, limit int64, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); errors.Is(err, io.EOF) {
		return true
	} else if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}
