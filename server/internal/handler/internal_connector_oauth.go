package handler

// OAuth connect flows of official app connectors. The API layer calls
//
//   - startConnectorOAuth    → authorize URL for one scope (workspace shared
//     account, a DingTalk group scene, or a person)
//   - completeConnectorOAuth → the provider callback: consume the state,
//     exchange the code, seal and store the credential for that scope, read
//     the account label, run the first tool discovery, and compute the
//     browser redirect.
//
// DCR apps redirect to <app origin>/api/connector-oauth/callback and reuse
// one dynamic client registration per connector (connector_oauth_client).
// GitHub uses the deployment's GitHub App and its registered callback
// <githubFrontend()>/api/github/authorize: its states carry the
// connectorOAuthStatePrefix, and GitHubAuthorizeCallback hands those to
// completeConnectorOAuth (Via connectorOAuthViaGitHub). States are 32 random
// bytes, stored only as SHA-256 hashes, single use, valid for 10 minutes.
//
// Browser binding: a state only completes in the browser that started it.
// The start response sets an HttpOnly, SameSite=Lax cookie holding a random
// nonce (named after the state hash, scoped to the callback path), and the
// sealed state records the nonce's hash; the callback refuses a state whose
// cookie is missing or different. Without it, anyone who may start a
// connect could send the provider authorize URL to a colleague, and the
// colleague's account would be stored in the sender's scope. Every start
// (workspace, scene and person scopes) binds the browser this way; there is
// no link that binds whichever browser opens it. The desktop app, whose API
// responses land in its own cookie jar, therefore never starts a connect: it
// opens the web connectors page in the system browser, and the admin
// connects there.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/connectorcatalog"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

const (
	// connectorOAuthStatePrefix marks connector OAuth states, so the shared
	// GitHub App callback can tell them from GitHub App install states.
	connectorOAuthStatePrefix    = "mcpc."
	connectorOAuthStateTTL       = 10 * time.Minute
	connectorOAuthCallbackPath   = "/api/connector-oauth/callback"
	connectorOAuthGitHubCallback = "/api/github/authorize"
	connectorOAuthScopeWorkspace = "workspace"
	connectorOAuthMaxReturnTo    = 2048
	// connectorOAuthCookiePrefix names the browser binding cookie of one
	// state: the prefix plus the first 16 hex characters of the state hash.
	connectorOAuthCookiePrefix = "multica_mcpc_"
	// connectorOAuthClientHistory is how many replaced dynamic client
	// registrations a connector keeps for refreshing the tokens they issued.
	connectorOAuthClientHistory = 8

	// Callback routes a state may complete on.
	connectorOAuthViaDCR    = "dcr"
	connectorOAuthViaGitHub = "github"
)

// Callback error codes appended as ?connect_error=<code>.
const (
	connectOAuthErrInvalidState = "invalid_state"
	// connectOAuthErrAccessDenied is only used when the provider answered
	// error=access_denied (the person declined); every other provider error
	// is connectOAuthErrProviderError.
	connectOAuthErrAccessDenied  = "access_denied"
	connectOAuthErrProviderError = "provider_error"
	// connectOAuthErrBrowserMismatch: the callback arrived in a browser that
	// did not start the connect (see "Browser binding" above).
	connectOAuthErrBrowserMismatch = "browser_mismatch"
	connectOAuthErrForbidden       = "forbidden"
	connectOAuthErrConnectorGone   = "connector_unavailable"
	connectOAuthErrExchangeFailed  = "exchange_failed"
	connectOAuthErrStoreFailed     = "store_failed"
	connectOAuthErrOAuthNotEnabled = "oauth_unavailable"
)

// connectorOAuthError is a start failure the API layer maps to Status.
type connectorOAuthError struct {
	Status  int
	Code    string
	Message string
}

func (e *connectorOAuthError) Error() string { return e.Message }

func oauthStartError(status int, code, message string) error {
	return &connectorOAuthError{Status: status, Code: code, Message: message}
}

// connectorOAuthScope names the credential an OAuth connect stores and who
// started it.
type connectorOAuthScope struct {
	WorkspaceID string
	ConnectorID string
	UserID      string
	// ScopeType is "workspace", "scene" or "person".
	ScopeType string
	// AgentID, OrgID and ScopeKey identify a scene or person scope (the
	// agent's current DingTalk org); they are empty for the workspace scope.
	AgentID  string
	OrgID    string
	ScopeKey string
}

// connectorOAuthStart is the input of startConnectorOAuth.
type connectorOAuthStart struct {
	connectorOAuthScope
	// ReturnTo is where the browser lands after the callback: a path, or an
	// absolute URL on the app origin (MULTICA_APP_URL / FRONTEND_ORIGIN).
	// Empty selects /dingtalk/configure?agent=<id> for scene and person
	// scopes and the workspace's connectors page for the workspace scope.
	ReturnTo string
}

// connectorOAuthStarted is the result of startConnectorOAuth.
type connectorOAuthStarted struct {
	// AuthorizeURL is the provider's authorize URL the browser goes to next.
	AuthorizeURL string
	// Cookie binds the state to the browser that receives the start
	// response; the API layer sets it.
	Cookie *http.Cookie
}

// connectorOAuthCallback is the input of completeConnectorOAuth.
type connectorOAuthCallback struct {
	// Via is connectorOAuthViaDCR (GET /api/connector-oauth/callback) or
	// connectorOAuthViaGitHub (delegated from GitHubAuthorizeCallback).
	Via   string
	State string
	Code  string
	// Error is the provider's "error" query parameter.
	Error string
	// BrowserNonce is the value of the state's browser binding cookie
	// ("" when the browser sent none).
	BrowserNonce string
}

// connectorOAuthOutcome is the result of completeConnectorOAuth.
// RedirectURL carries ?connected=<slug> or ?connect_error=<code>; it is ""
// only when the state is unknown, expired or replayed (ErrorCode
// "invalid_state"), in which case there is no trusted destination and the
// API layer answers with an error page instead of redirecting.
type connectorOAuthOutcome struct {
	RedirectURL string
	ErrorCode   string
	ConnectorID string
	Slug        string
	ScopeType   string
	Account     string
	// Discovered is the number of tools discovered right after connecting
	// (0 when tools were already known or discovery failed).
	Discovered int
}

// connectorOAuthAppOrigin is the browser-facing origin OAuth callbacks and
// return pages live on ("" when none is configured).
func (h *Handler) connectorOAuthAppOrigin() string {
	return strings.TrimRight(firstNonEmpty(h.currentConfig().AppURL, h.currentConfig().FrontendOrigin), "/")
}

// startConnectorOAuth checks that in.UserID may connect an account for the
// scope (authorizeConnectorOAuthScope), prepares the provider authorization
// (reusing or creating the DCR client registration), stores a single-use
// state with the sealed PKCE verifier and browser binding, and returns where
// the browser goes next plus the binding cookie. Errors are
// *connectorOAuthError.
func (h *Handler) startConnectorOAuth(ctx context.Context, in connectorOAuthStart) (connectorOAuthStarted, error) {
	internalErr := oauthStartError(http.StatusInternalServerError, "internal", "could not start the connection")
	scope, err := normalizeConnectorOAuthScope(in.connectorOAuthScope)
	if err != nil {
		return connectorOAuthStarted{}, oauthStartError(http.StatusBadRequest, "invalid_scope", "invalid connection scope")
	}
	c, err := h.loadInternalConnector(ctx, scope.WorkspaceID, scope.ConnectorID)
	if errors.Is(err, errConnectorNotFound) {
		return connectorOAuthStarted{}, oauthStartError(http.StatusNotFound, "not_found", "connector not found")
	}
	if err != nil {
		return connectorOAuthStarted{}, oauthStartError(http.StatusInternalServerError, "internal", "connector lookup failed")
	}
	app, ok := catalogApp(c.CatalogSlug)
	if !ok || c.AuthMode != "oauth" || c.UpstreamURL != app.MCPURL {
		return connectorOAuthStarted{}, oauthStartError(http.StatusBadRequest, "not_oauth", "this connector does not connect through OAuth")
	}
	if err := h.authorizeConnectorOAuthScope(ctx, scope, c); err != nil {
		return connectorOAuthStarted{}, err
	}
	// Deployment configuration is reported only to callers allowed to
	// connect this scope.
	if err := h.connectorOAuthDeploymentError(app); err != nil {
		return connectorOAuthStarted{}, err
	}
	origin := h.connectorOAuthAppOrigin()
	returnTo, err := h.connectorOAuthReturnTo(ctx, in.ReturnTo, scope)
	if err != nil {
		return connectorOAuthStarted{}, oauthStartError(http.StatusBadRequest, "invalid_return_to", "return_to must be on the app origin")
	}
	state, err := randomOAuthValue()
	if err != nil {
		return connectorOAuthStarted{}, internalErr
	}
	state = connectorOAuthStatePrefix + state
	verifier, err := randomOAuthValue()
	if err != nil {
		return connectorOAuthStarted{}, internalErr
	}
	payload := connectorSealedVerifier{StateHash: hashConnectorOAuthState(state), ConnectorID: scope.ConnectorID, Verifier: verifier}
	var authorizeURL string
	switch app.AuthKind {
	case connectorcatalog.AuthOAuthGitHubApp:
		payload.Via, payload.ClientID = connectorOAuthViaGitHub, strings.TrimSpace(os.Getenv("GITHUB_APP_CLIENT_ID"))
		authorizeURL, err = githubConnectorAuthorizeURL(app, h.githubFrontend()+connectorOAuthGitHubCallback, state, verifier)
	case connectorcatalog.AuthOAuthDCR:
		var record connectorOAuthClientRecord
		record, err = h.ensureConnectorOAuthClient(ctx, c, app, origin+connectorOAuthCallbackPath)
		if err == nil {
			payload.Via, payload.ClientID = connectorOAuthViaDCR, record.Registration.ClientID
			authorizeURL, err = remotemcp.BuildAuthorizationURL(remotemcp.OAuthMetadata{
				ResourceEndpoint: record.Resource, AuthorizationEndpoint: record.AuthorizationEndpoint,
			}, record.Registration, record.RedirectURI, state, verifier, record.Scope)
		}
	default:
		err = errConnectorNotCatalog
	}
	if err != nil {
		slog.WarnContext(ctx, "official app OAuth start failed", "connector_id", c.ID, "catalog_slug", c.CatalogSlug, "error", err)
		return connectorOAuthStarted{}, oauthStartError(http.StatusBadGateway, "provider_unavailable", "the app's authorization server could not be prepared")
	}
	nonce, err := randomOAuthValue()
	if err != nil {
		return connectorOAuthStarted{}, internalErr
	}
	payload.BrowserHash = hashConnectorOAuthState(nonce)
	callbackOrigin, callbackPath := h.connectorOAuthCallbackTarget(payload.Via)
	started := connectorOAuthStarted{
		AuthorizeURL: authorizeURL,
		Cookie:       connectorOAuthBrowserCookie(payload.StateHash, nonce, callbackOrigin, callbackPath),
	}
	if err := h.insertConnectorOAuthState(ctx, payload, scope, returnTo); err != nil {
		slog.ErrorContext(ctx, "official app OAuth state insert failed", "connector_id", c.ID, "error", err)
		return connectorOAuthStarted{}, internalErr
	}
	slog.InfoContext(ctx, "official app OAuth started", "connector_id", c.ID, "catalog_slug", c.CatalogSlug, "scope_type", scope.ScopeType,
		"user_id", scope.UserID)
	return started, nil
}

// connectorOAuthCallbackTarget returns the origin and path the provider
// redirects a connect of the given route to: the GitHub App's registered
// callback on FRONTEND_ORIGIN, or the DCR callback on the app origin. The
// browser binding cookie must live there.
func (h *Handler) connectorOAuthCallbackTarget(via string) (string, string) {
	if via == connectorOAuthViaGitHub {
		return h.githubFrontend(), connectorOAuthGitHubCallback
	}
	return h.connectorOAuthAppOrigin(), connectorOAuthCallbackPath
}

// connectorOAuthCookieName is the name of the browser binding cookie of the
// state with stateHash (hashConnectorOAuthState).
func connectorOAuthCookieName(stateHash string) string {
	if len(stateHash) > 16 {
		stateHash = stateHash[:16]
	}
	return connectorOAuthCookiePrefix + stateHash
}

// connectorOAuthBrowserCookie is the HttpOnly, SameSite=Lax binding cookie
// of one state, scoped to its callback path and living as long as the state.
// An empty value clears it.
func connectorOAuthBrowserCookie(stateHash, value, origin, path string) *http.Cookie {
	cookie := &http.Cookie{
		Name: connectorOAuthCookieName(stateHash), Value: value, Path: path, HttpOnly: true,
		Secure: strings.HasPrefix(origin, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: int(connectorOAuthStateTTL.Seconds()),
	}
	if value == "" {
		cookie.MaxAge = -1
	}
	return cookie
}

func normalizeConnectorOAuthScope(in connectorOAuthScope) (connectorOAuthScope, error) {
	out := in
	for _, field := range []*string{&out.WorkspaceID, &out.ConnectorID, &out.UserID} {
		id, err := canonicalOAuthUUID(*field)
		if err != nil {
			return out, err
		}
		*field = id
	}
	switch out.ScopeType {
	case connectorOAuthScopeWorkspace:
		out.AgentID, out.OrgID, out.ScopeKey = "", "", ""
	case contextcap.ScopeScene, contextcap.ScopePerson:
		id, err := canonicalOAuthUUID(out.AgentID)
		if err != nil {
			return out, err
		}
		out.AgentID = id
		if !contextcap.ValidScopeKey(out.ScopeType, out.ScopeKey) {
			return out, errors.New("invalid scope key")
		}
	default:
		return out, errors.New("invalid scope type")
	}
	return out, nil
}

func canonicalOAuthUUID(raw string) (string, error) {
	id, err := parseStrictUUID(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// authorizeConnectorOAuthScope re-checks, at start and again at callback,
// that scope.UserID may store a credential for the scope: a workspace
// owner/admin for the workspace scope; for a scene or person scope a live
// grant under the agent's current org (for a scene, managing the agent and
// the scene being one the agent has seen also qualifies, as on the other
// mobile routes) and a connector that is enabled and offered to the agent
// or, for a person scope, globally granted to it (the PUT credential rule,
// see contextCapCredentialConnector). Errors are *connectorOAuthError.
func (h *Handler) authorizeConnectorOAuthScope(ctx context.Context, scope connectorOAuthScope, c internalConnector) error {
	forbidden := oauthStartError(http.StatusForbidden, connectOAuthErrForbidden, "you are not allowed to connect an account for this scope")
	if scope.ScopeType == connectorOAuthScopeWorkspace {
		member, err := h.getWorkspaceMember(ctx, scope.UserID, scope.WorkspaceID)
		if err != nil || !roleAllowed(member.Role, "owner", "admin") {
			return forbidden
		}
		return nil
	}
	agent, err := h.loadContextCapAgent(ctx, scope.AgentID)
	if errors.Is(err, contextcap.ErrNotFound) || (err == nil && (agent.WorkspaceID != scope.WorkspaceID || agent.OrgID != scope.OrgID)) {
		return forbidden
	}
	if err != nil {
		return oauthStartError(http.StatusInternalServerError, "internal", "agent lookup failed")
	}
	// The mobile routes' authority (contextCapResolveScope); a malformed
	// scope and a manager's unknown scene are forbidden here.
	if _, err := h.contextCapResolveScope(ctx, agent, scope.UserID, scope.ScopeType, scope.ScopeKey); err != nil {
		switch {
		case errors.Is(err, errContextCapForbidden), errors.Is(err, contextcap.ErrInvalidInput), errors.Is(err, contextcap.ErrNotFound):
			return forbidden
		case errors.Is(err, errContextCapSceneLookup):
			return oauthStartError(http.StatusInternalServerError, "internal", "scene lookup failed")
		default:
			return oauthStartError(http.StatusInternalServerError, "internal", "grant lookup failed")
		}
	}
	if !c.Enabled {
		return forbidden
	}
	offered, err := contextcap.IsOffered(ctx, h.DB, scope.WorkspaceID, scope.AgentID, contextcap.ResourceConnector, scope.ConnectorID)
	if err != nil {
		return oauthStartError(http.StatusInternalServerError, "internal", "offer lookup failed")
	}
	if offered {
		return nil
	}
	if scope.ScopeType != contextcap.ScopePerson {
		return forbidden
	}
	var granted bool
	if err := h.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM internal_connector_agent WHERE connector_id = $1::uuid AND workspace_id = $2::uuid AND agent_id = $3::uuid)`,
		scope.ConnectorID, scope.WorkspaceID, scope.AgentID).Scan(&granted); err != nil {
		return oauthStartError(http.StatusInternalServerError, "internal", "grant lookup failed")
	}
	if !granted {
		return forbidden
	}
	return nil
}

// connectorOAuthReturnTo validates a client-supplied destination: a path
// ("/…", not "//…") resolved against the app origin, or an absolute http(s)
// URL on one of the app origins. Empty yields the scope's default page.
func (h *Handler) connectorOAuthReturnTo(ctx context.Context, raw string, scope connectorOAuthScope) (string, error) {
	origin := h.connectorOAuthAppOrigin()
	if origin == "" {
		return "", errors.New("no app origin")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if scope.ScopeType == connectorOAuthScopeWorkspace {
			path := "/"
			if h.Queries == nil {
				return origin + path, nil
			}
			if workspace, err := h.Queries.GetWorkspace(ctx, parseUUID(scope.WorkspaceID)); err == nil {
				path = "/" + url.PathEscape(workspace.Slug) + "/internal-connectors"
			}
			return origin + path, nil
		}
		return origin + "/dingtalk/configure?agent=" + url.QueryEscape(scope.AgentID), nil
	}
	if len(raw) > connectorOAuthMaxReturnTo || strings.ContainsAny(raw, "\r\n\x00\\") {
		return "", errors.New("invalid return_to")
	}
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		raw = origin + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", errors.New("invalid return_to")
	}
	candidate := strings.ToLower(u.Scheme + "://" + u.Host)
	for _, allowed := range h.dingTalkJSAPIAppOrigins() {
		if candidate == allowed {
			u.Fragment = ""
			return u.String(), nil
		}
	}
	return "", errors.New("return_to is not on the app origin")
}

// withOAuthResult sets one result parameter (connected / connect_error) on
// a validated destination, dropping any earlier result parameters.
func withOAuthResult(destination, key, value string) string {
	u, err := url.Parse(destination)
	if err != nil {
		return destination
	}
	query := u.Query()
	query.Del("connected")
	query.Del("connect_error")
	query.Set(key, value)
	u.RawQuery = query.Encode()
	return u.String()
}

func randomOAuthValue() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func hashConnectorOAuthState(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}

// validConnectorOAuthState accepts exactly the prefix plus 43 base64url
// characters minted by startConnectorOAuth.
func validConnectorOAuthState(state string) bool {
	body, ok := strings.CutPrefix(state, connectorOAuthStatePrefix)
	if !ok || len(body) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(body)
	return err == nil
}

// isConnectorOAuthState reports whether a GitHub callback state belongs to
// the connector flow (GitHubAuthorizeCallback dispatches on it).
func isConnectorOAuthState(state string) bool {
	return strings.HasPrefix(state, connectorOAuthStatePrefix)
}

func githubConnectorAuthorizeURL(app connectorcatalog.App, redirectURI, state, verifier string) (string, error) {
	endpoint, err := url.Parse(app.AuthorizationEndpoint)
	if err != nil {
		return "", err
	}
	challenge := sha256.Sum256([]byte(verifier))
	query := endpoint.Query()
	query.Set("client_id", strings.TrimSpace(os.Getenv("GITHUB_APP_CLIENT_ID")))
	query.Set("redirect_uri", redirectURI)
	query.Set("state", state)
	query.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	query.Set("code_challenge_method", "S256")
	if app.Scope != "" {
		query.Set("scope", app.Scope)
	}
	endpoint.RawQuery = query.Encode()
	return endpoint.String(), nil
}

// connectorSealedVerifier is the sealed part of one state: the PKCE
// verifier, the OAuth client and route it was started with, and its browser
// binding.
type connectorSealedVerifier struct {
	StateHash   string `json:"state_hash"`
	ConnectorID string `json:"connector_id"`
	Verifier    string `json:"verifier"`
	// Via is the callback route the state completes on.
	Via string `json:"via"`
	// ClientID is the OAuth client the authorize URL was built for; the code
	// is exchanged with that client.
	ClientID string `json:"client_id"`
	// BrowserHash is the SHA-256 (hex) of the browser binding nonce.
	BrowserHash string `json:"browser_hash,omitempty"`
}

func (h *Handler) sealConnectorOAuthVerifier(payload connectorSealedVerifier) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return h.InternalConnectorSecretBox.Seal(raw)
}

// openConnectorOAuthVerifier opens the sealed part of the state with
// stateHash of connectorID.
func (h *Handler) openConnectorOAuthVerifier(sealed []byte, stateHash, connectorID string) (connectorSealedVerifier, bool) {
	var payload connectorSealedVerifier
	plain, err := h.InternalConnectorSecretBox.Open(sealed)
	if err != nil || json.Unmarshal(plain, &payload) != nil || payload.StateHash != stateHash || payload.ConnectorID != connectorID || payload.Verifier == "" {
		return connectorSealedVerifier{}, false
	}
	return payload, true
}

func (h *Handler) insertConnectorOAuthState(ctx context.Context, payload connectorSealedVerifier, scope connectorOAuthScope, returnTo string) error {
	sealed, err := h.sealConnectorOAuthVerifier(payload)
	if err != nil {
		return err
	}
	// Opportunistic cleanup keeps the table small without a sweeper.
	if _, err := h.DB.Exec(ctx, `DELETE FROM connector_oauth_state WHERE state_hash IN (
		SELECT state_hash FROM connector_oauth_state WHERE expires_at < now() LIMIT 200)`); err != nil {
		slog.WarnContext(ctx, "connector OAuth state cleanup failed", "error", err)
	}
	var agentID any
	if scope.AgentID != "" {
		agentID = scope.AgentID
	}
	_, err = h.DB.Exec(ctx, `INSERT INTO connector_oauth_state
		(state_hash, workspace_id, connector_id, agent_id, scope_type, org_id, scope_key, user_id, verifier_ciphertext, return_to, expires_at)
		VALUES ($1, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, $8::uuid, $9, $10, now() + make_interval(secs => $11))`,
		payload.StateHash, scope.WorkspaceID, scope.ConnectorID, agentID, scope.ScopeType, scope.OrgID, scope.ScopeKey, scope.UserID,
		sealed, returnTo, connectorOAuthStateTTL.Seconds())
	return err
}

// consumedConnectorOAuthState is a state row consumed by a callback.
type consumedConnectorOAuthState struct {
	scope     connectorOAuthScope
	stateHash string
	verifier  []byte
	returnTo  string
}

// consumeConnectorOAuthState atomically marks an unexpired, unconsumed
// state consumed and returns it, or pgx.ErrNoRows.
func (h *Handler) consumeConnectorOAuthState(ctx context.Context, state string) (consumedConnectorOAuthState, error) {
	out := consumedConnectorOAuthState{stateHash: hashConnectorOAuthState(state)}
	var agentID *string
	err := h.DB.QueryRow(ctx, `UPDATE connector_oauth_state SET consumed_at = now()
		WHERE state_hash = $1 AND consumed_at IS NULL AND expires_at > now()
		RETURNING workspace_id::text, connector_id::text, agent_id::text, scope_type, org_id, scope_key, user_id::text, verifier_ciphertext, return_to`,
		out.stateHash).Scan(&out.scope.WorkspaceID, &out.scope.ConnectorID, &agentID, &out.scope.ScopeType, &out.scope.OrgID,
		&out.scope.ScopeKey, &out.scope.UserID, &out.verifier, &out.returnTo)
	if agentID != nil {
		out.scope.AgentID = *agentID
	}
	return out, err
}

// completeConnectorOAuth finishes a connect. See connectorOAuthOutcome for
// the result; no error is returned because every failure becomes a
// connect_error redirect (or an unknown-state outcome). The state is
// consumed first, so a refused callback also burns it.
func (h *Handler) completeConnectorOAuth(ctx context.Context, in connectorOAuthCallback) connectorOAuthOutcome {
	out := connectorOAuthOutcome{ErrorCode: connectOAuthErrInvalidState}
	if !validConnectorOAuthState(in.State) || h.InternalConnectorSecretBox == nil {
		return out
	}
	state, err := h.consumeConnectorOAuthState(ctx, in.State)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.ErrorContext(ctx, "connector OAuth state lookup failed", "error", err)
		}
		return out
	}
	scope := state.scope
	out.ConnectorID, out.ScopeType = scope.ConnectorID, scope.ScopeType
	fail := func(code string) connectorOAuthOutcome {
		out.ErrorCode = code
		out.RedirectURL = withOAuthResult(state.returnTo, "connect_error", code)
		slog.InfoContext(ctx, "official app OAuth connect failed", "connector_id", scope.ConnectorID, "scope_type", scope.ScopeType, "user_id", scope.UserID, "error_code", code)
		return out
	}
	c, err := h.loadInternalConnector(ctx, scope.WorkspaceID, scope.ConnectorID)
	if err != nil {
		return fail(connectOAuthErrConnectorGone)
	}
	app, ok := catalogApp(c.CatalogSlug)
	if !ok || c.AuthMode != "oauth" {
		return fail(connectOAuthErrConnectorGone)
	}
	out.Slug = app.Slug
	wantVia := connectorOAuthViaDCR
	if app.AuthKind == connectorcatalog.AuthOAuthGitHubApp {
		wantVia = connectorOAuthViaGitHub
	}
	verifier, ok := h.openConnectorOAuthVerifier(state.verifier, state.stateHash, scope.ConnectorID)
	if !ok || in.Via != wantVia || verifier.Via != wantVia {
		return fail(connectOAuthErrInvalidState)
	}
	// Only the browser that started the connect may complete it: the code
	// must never land in another scope.
	if verifier.BrowserHash == "" || in.BrowserNonce == "" ||
		!hmac.Equal([]byte(hashConnectorOAuthState(in.BrowserNonce)), []byte(verifier.BrowserHash)) {
		return fail(connectOAuthErrBrowserMismatch)
	}
	if in.Error == "access_denied" {
		return fail(connectOAuthErrAccessDenied)
	}
	if in.Error != "" || in.Code == "" {
		slog.InfoContext(ctx, "official app provider returned an error", "connector_id", c.ID, "catalog_slug", app.Slug, "provider_error", contextcap.SanitizeAccount(in.Error))
		return fail(connectOAuthErrProviderError)
	}
	if err := h.authorizeConnectorOAuthScope(ctx, scope, c); err != nil {
		return fail(connectOAuthErrForbidden)
	}
	endpoint, err := h.connectorTokenEndpoint(ctx, c, verifier.ClientID)
	if err != nil {
		slog.WarnContext(ctx, "official app OAuth token endpoint unavailable", "connector_id", c.ID, "catalog_slug", app.Slug, "error", err)
		return fail(connectOAuthErrExchangeFailed)
	}
	exchangeCtx, cancel := context.WithTimeout(ctx, connectorOAuthHTTPTimeout)
	defer cancel()
	response, err := endpoint.client.ExchangeOAuthCode(exchangeCtx, endpoint.tokenURL, endpoint.resource, in.Code, endpoint.redirectURI, verifier.Verifier, endpoint.registration)
	if err != nil {
		slog.WarnContext(ctx, "official app OAuth code exchange failed", "connector_id", c.ID, "catalog_slug", app.Slug, "error", err)
		return fail(connectOAuthErrExchangeFailed)
	}
	now := time.Now()
	token := contextcap.OAuthToken{
		AccessToken: response.AccessToken, RefreshToken: response.RefreshToken, TokenType: "bearer", Scope: response.Scope,
		ClientID: endpoint.registration.ClientID,
	}
	if expiry := remotemcp.OAuthExpiry(now, response.ExpiresIn); !expiry.IsZero() {
		token.ExpiresAt = expiry.Unix()
	}
	token.Account = h.connectorOAuthAccount(exchangeCtx, app, response.AccessToken)
	if !token.Valid() {
		return fail(connectOAuthErrExchangeFailed)
	}
	secret := contextcap.Secret{Bearer: token.AccessToken, OAuth: &token}
	layer, key, err := h.storeConnectorOAuthCredential(ctx, scope, secret)
	if err != nil {
		slog.ErrorContext(ctx, "official app OAuth credential store failed", "connector_id", c.ID, "scope_type", scope.ScopeType, "error", err)
		return fail(connectOAuthErrStoreFailed)
	}
	out.Account = token.Account
	h.enableConnectedScopeBinding(ctx, scope)
	if result, ran, err := h.discoverCatalogToolsIfEmpty(ctx, scope.WorkspaceID, scope.ConnectorID, secret, layer, key); err != nil {
		slog.WarnContext(ctx, "official app first tool discovery failed", "connector_id", c.ID, "catalog_slug", app.Slug, "error", err)
	} else if ran {
		out.Discovered = result.Discovered
	}
	out.ErrorCode = ""
	out.RedirectURL = withOAuthResult(state.returnTo, "connected", app.Slug)
	slog.InfoContext(ctx, "official app OAuth connected", "connector_id", c.ID, "catalog_slug", app.Slug, "scope_type", scope.ScopeType, "user_id", scope.UserID)
	return out
}

// storeConnectorOAuthCredential seals and stores secret for scope and
// returns its credential layer and (for scene/person) row key.
func (h *Handler) storeConnectorOAuthCredential(ctx context.Context, scope connectorOAuthScope, secret contextcap.Secret) (string, contextcap.CredentialBinding, error) {
	if scope.ScopeType == connectorOAuthScopeWorkspace {
		sealed, err := h.sealWorkspaceConnectorSecret(scope.WorkspaceID, scope.ConnectorID, secret)
		if err != nil {
			return "", contextcap.CredentialBinding{}, err
		}
		tag, err := h.DB.Exec(ctx, `UPDATE internal_connector SET credential_ciphertext = $3, updated_at = now()
			WHERE id = $1::uuid AND workspace_id = $2::uuid`, scope.ConnectorID, scope.WorkspaceID, sealed)
		if err != nil {
			return "", contextcap.CredentialBinding{}, err
		}
		if tag.RowsAffected() != 1 {
			return "", contextcap.CredentialBinding{}, errConnectorNotFound
		}
		return connectorCredentialWorkspace, contextcap.CredentialBinding{}, nil
	}
	key := contextcap.CredentialBinding{
		WorkspaceID: scope.WorkspaceID, AgentID: scope.AgentID, ConnectorID: scope.ConnectorID,
		ScopeType: scope.ScopeType, OrgID: scope.OrgID, ScopeKey: scope.ScopeKey,
	}
	sealed, err := contextcap.SealOAuthCredential(h.contextCredentialBox(), key, *secret.OAuth)
	if err != nil {
		return "", contextcap.CredentialBinding{}, err
	}
	if _, err := contextcap.UpsertCredential(ctx, h.DB, key, sealed, contextcap.OAuthHint(secret.OAuth.Account), scope.UserID); err != nil {
		return "", contextcap.CredentialBinding{}, err
	}
	return scope.ScopeType, key, nil
}

// enableConnectedScopeBinding turns the connector on for the scene or person
// scope that just connected an account ("连接" means "use it here"). It only
// applies to offered connectors; a person connecting a globally granted,
// unoffered connector already gets it through the global grant. Failures are
// logged: the credential is stored and the toggle stays available.
func (h *Handler) enableConnectedScopeBinding(ctx context.Context, scope connectorOAuthScope) {
	if scope.ScopeType != contextcap.ScopeScene && scope.ScopeType != contextcap.ScopePerson {
		return
	}
	_, err := contextcap.UpsertBinding(ctx, h.DB, contextcap.BindingWrite{
		WorkspaceID: scope.WorkspaceID, AgentID: scope.AgentID, ScopeType: scope.ScopeType, OrgID: scope.OrgID, ScopeKey: scope.ScopeKey,
		ResourceType: contextcap.ResourceConnector, ResourceID: scope.ConnectorID, Enabled: true, ActorID: scope.UserID,
	})
	if err != nil && !errors.Is(err, contextcap.ErrNotOffered) {
		slog.WarnContext(ctx, "official app connect: scope binding not enabled", "connector_id", scope.ConnectorID, "scope_type", scope.ScopeType, "error", err)
	}
}

// connectorOAuthAccount reads the provider account label with a fresh
// token (GitHub: GET /user → login). Best effort: "" on any failure.
func (h *Handler) connectorOAuthAccount(ctx context.Context, app connectorcatalog.App, accessToken string) string {
	if app.AccountURL == "" {
		return ""
	}
	var profile struct {
		Login string `json:"login"`
	}
	headers := bearerHeader(accessToken)
	headers.Set("X-GitHub-Api-Version", "2022-11-28")
	if err := catalogExternalClient(app).GetJSON(ctx, app.AccountURL, headers, &profile); err != nil {
		slog.InfoContext(ctx, "official app account lookup failed", "catalog_slug", app.Slug, "error", err)
		return ""
	}
	return contextcap.SanitizeAccount(profile.Login)
}

// connectorOAuthRegistration is one dynamic client registration of a
// connector with the endpoints it was registered for.
type connectorOAuthRegistration struct {
	AuthorizationEndpoint string
	TokenEndpoint         string
	Resource              string
	RedirectURI           string
	Registration          remotemcp.OAuthClientRegistration
}

// connectorOAuthClientRecord is one row of connector_oauth_client with its
// opened registrations: the current one (used to start connects) and the
// earlier ones, newest first, kept so tokens they issued can still be
// exchanged and refreshed after the registration was replaced.
type connectorOAuthClientRecord struct {
	Issuer                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	Resource              string
	Scope                 string
	RedirectURI           string
	Registration          remotemcp.OAuthClientRegistration
	Previous              []connectorOAuthRegistration
}

// current returns the record's current registration.
func (r connectorOAuthClientRecord) current() connectorOAuthRegistration {
	return connectorOAuthRegistration{
		AuthorizationEndpoint: r.AuthorizationEndpoint, TokenEndpoint: r.TokenEndpoint, Resource: r.Resource,
		RedirectURI: r.RedirectURI, Registration: r.Registration,
	}
}

// registrationFor returns the registration of clientID ("" = the current
// one), or false when the connector no longer knows that client.
func (r connectorOAuthClientRecord) registrationFor(clientID string) (connectorOAuthRegistration, bool) {
	if clientID == "" || clientID == r.Registration.ClientID {
		return r.current(), true
	}
	for _, previous := range r.Previous {
		if previous.Registration.ClientID == clientID {
			return previous, true
		}
	}
	return connectorOAuthRegistration{}, false
}

// connectorSealedOAuthClient is the sealed DCR registration of a connector.
type connectorSealedOAuthClient struct {
	WorkspaceID             string `json:"workspace_id"`
	ConnectorID             string `json:"connector_id"`
	ClientID                string `json:"client_id"`
	ClientSecret            string `json:"client_secret,omitempty"`
	TokenEndpointAuthMethod string `json:"token_endpoint_auth_method"`
	// Previous are the connector's earlier registrations, newest first, at
	// most connectorOAuthClientHistory.
	Previous []connectorSealedOAuthRegistration `json:"previous,omitempty"`
}

// connectorSealedOAuthRegistration is one earlier registration inside
// connectorSealedOAuthClient.
type connectorSealedOAuthRegistration struct {
	ClientID                string `json:"client_id"`
	ClientSecret            string `json:"client_secret,omitempty"`
	TokenEndpointAuthMethod string `json:"token_endpoint_auth_method"`
	AuthorizationEndpoint   string `json:"authorization_endpoint"`
	TokenEndpoint           string `json:"token_endpoint"`
	Resource                string `json:"resource,omitempty"`
	RedirectURI             string `json:"redirect_uri"`
}

func sealedOAuthRegistration(r connectorOAuthRegistration) connectorSealedOAuthRegistration {
	return connectorSealedOAuthRegistration{
		ClientID: r.Registration.ClientID, ClientSecret: r.Registration.ClientSecret, TokenEndpointAuthMethod: r.Registration.TokenEndpointAuthMethod,
		AuthorizationEndpoint: r.AuthorizationEndpoint, TokenEndpoint: r.TokenEndpoint, Resource: r.Resource, RedirectURI: r.RedirectURI,
	}
}

func openedOAuthRegistration(s connectorSealedOAuthRegistration) connectorOAuthRegistration {
	return connectorOAuthRegistration{
		AuthorizationEndpoint: s.AuthorizationEndpoint, TokenEndpoint: s.TokenEndpoint, Resource: s.Resource, RedirectURI: s.RedirectURI,
		Registration: remotemcp.OAuthClientRegistration{ClientID: s.ClientID, ClientSecret: s.ClientSecret, TokenEndpointAuthMethod: s.TokenEndpointAuthMethod},
	}
}

func (h *Handler) loadConnectorOAuthClient(ctx context.Context, workspaceID, connectorID string) (connectorOAuthClientRecord, error) {
	return h.loadConnectorOAuthClientWith(ctx, h.DB, workspaceID, connectorID)
}

func (h *Handler) loadConnectorOAuthClientWith(ctx context.Context, q contextcap.DBTX, workspaceID, connectorID string) (connectorOAuthClientRecord, error) {
	var out connectorOAuthClientRecord
	var sealed []byte
	err := q.QueryRow(ctx, `SELECT issuer, authorization_endpoint, token_endpoint, resource, scope, redirect_uri, registration_ciphertext
		FROM connector_oauth_client WHERE connector_id = $1::uuid AND workspace_id = $2::uuid`, connectorID, workspaceID).
		Scan(&out.Issuer, &out.AuthorizationEndpoint, &out.TokenEndpoint, &out.Resource, &out.Scope, &out.RedirectURI, &sealed)
	if err != nil {
		return out, err
	}
	if h.InternalConnectorSecretBox == nil {
		return out, errors.New("connector credential key unavailable")
	}
	plain, err := h.InternalConnectorSecretBox.Open(sealed)
	var registration connectorSealedOAuthClient
	if err != nil || json.Unmarshal(plain, &registration) != nil || registration.WorkspaceID != workspaceID || registration.ConnectorID != connectorID || registration.ClientID == "" {
		return out, errors.New("connector OAuth client registration unavailable")
	}
	out.Registration = remotemcp.OAuthClientRegistration{
		ClientID: registration.ClientID, ClientSecret: registration.ClientSecret, TokenEndpointAuthMethod: registration.TokenEndpointAuthMethod,
	}
	for _, previous := range registration.Previous {
		if previous.ClientID != "" {
			out.Previous = append(out.Previous, openedOAuthRegistration(previous))
		}
	}
	return out, nil
}

// ensureConnectorOAuthClient returns the connector's DCR registration for
// redirectURI, discovering the authorization server and registering a
// client once. Registration is serialized per connector with an advisory
// lock so concurrent starts on any replica register once.
//
// When the app origin (and so the redirect URI) changed, the registration
// for the new redirect URI becomes current: a kept earlier registration for
// that URI is promoted back, otherwise a new client is registered. The
// replaced registration is kept (connectorOAuthClientHistory), because
// every token it issued can only be refreshed by it; overwriting it would
// turn each connection's next refresh into invalid_grant and delete it.
func (h *Handler) ensureConnectorOAuthClient(ctx context.Context, c internalConnector, app connectorcatalog.App, redirectURI string) (connectorOAuthClientRecord, error) {
	if record, err := h.loadConnectorOAuthClient(ctx, c.WorkspaceID, c.ID); err == nil && record.RedirectURI == redirectURI {
		return record, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*connectorOAuthHTTPTimeout)
	defer cancel()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return connectorOAuthClientRecord{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('connector_oauth_client:' || $1::text, 0))`, c.ID); err != nil {
		return connectorOAuthClientRecord{}, err
	}
	existing, existingErr := h.loadConnectorOAuthClientWith(ctx, tx, c.WorkspaceID, c.ID)
	if existingErr == nil && existing.RedirectURI == redirectURI {
		return existing, nil
	}
	var history []connectorOAuthRegistration
	if existingErr == nil {
		history = append([]connectorOAuthRegistration{existing.current()}, existing.Previous...)
	}
	var next connectorOAuthRegistration
	promoted := false
	for i, previous := range history {
		if previous.RedirectURI == redirectURI && previous.AuthorizationEndpoint != "" && previous.TokenEndpoint != "" {
			next, promoted = previous, true
			history = append(history[:i:i], history[i+1:]...)
			break
		}
	}
	if !promoted {
		client := catalogExternalClient(app)
		metadata, err := client.DiscoverOAuth(ctx, app.MCPURL)
		if err != nil {
			return connectorOAuthClientRecord{}, fmt.Errorf("discover OAuth metadata: %w", err)
		}
		registration, err := client.RegisterOAuthClient(ctx, metadata, redirectURI, "Multica")
		if err != nil {
			return connectorOAuthClientRecord{}, err
		}
		next = connectorOAuthRegistration{
			AuthorizationEndpoint: metadata.AuthorizationEndpoint, TokenEndpoint: metadata.TokenEndpoint,
			Resource: metadata.ResourceEndpoint, RedirectURI: redirectURI, Registration: registration,
		}
	}
	kept := make([]connectorOAuthRegistration, 0, connectorOAuthClientHistory)
	sealedHistory := make([]connectorSealedOAuthRegistration, 0, connectorOAuthClientHistory)
	for _, previous := range history {
		if len(kept) == connectorOAuthClientHistory || previous.Registration.ClientID == next.Registration.ClientID {
			continue
		}
		kept = append(kept, previous)
		sealedHistory = append(sealedHistory, sealedOAuthRegistration(previous))
	}
	payload, err := json.Marshal(connectorSealedOAuthClient{
		WorkspaceID: c.WorkspaceID, ConnectorID: c.ID, ClientID: next.Registration.ClientID,
		ClientSecret: next.Registration.ClientSecret, TokenEndpointAuthMethod: next.Registration.TokenEndpointAuthMethod,
		Previous: sealedHistory,
	})
	if err != nil {
		return connectorOAuthClientRecord{}, err
	}
	sealed, err := h.InternalConnectorSecretBox.Seal(payload)
	if err != nil {
		return connectorOAuthClientRecord{}, err
	}
	record := connectorOAuthClientRecord{
		AuthorizationEndpoint: next.AuthorizationEndpoint, TokenEndpoint: next.TokenEndpoint, Resource: next.Resource,
		Scope: app.Scope, RedirectURI: redirectURI, Registration: next.Registration, Previous: kept,
	}
	if issuer, err := url.Parse(next.AuthorizationEndpoint); err == nil {
		record.Issuer = issuer.Scheme + "://" + issuer.Host
	}
	if _, err := tx.Exec(ctx, `INSERT INTO connector_oauth_client
		(connector_id, workspace_id, issuer, authorization_endpoint, token_endpoint, resource, scope, redirect_uri, registration_ciphertext)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (connector_id) DO UPDATE SET issuer = EXCLUDED.issuer, authorization_endpoint = EXCLUDED.authorization_endpoint,
			token_endpoint = EXCLUDED.token_endpoint, resource = EXCLUDED.resource, scope = EXCLUDED.scope,
			redirect_uri = EXCLUDED.redirect_uri, registration_ciphertext = EXCLUDED.registration_ciphertext, updated_at = now()
		WHERE connector_oauth_client.workspace_id = EXCLUDED.workspace_id`,
		c.ID, c.WorkspaceID, record.Issuer, record.AuthorizationEndpoint, record.TokenEndpoint, record.Resource, record.Scope,
		record.RedirectURI, sealed); err != nil {
		return connectorOAuthClientRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return connectorOAuthClientRecord{}, err
	}
	slog.InfoContext(ctx, "official app OAuth client registered", "connector_id", c.ID, "catalog_slug", app.Slug,
		"token_endpoint_auth_method", next.Registration.TokenEndpointAuthMethod, "reused_previous", promoted, "kept_previous", len(kept))
	return record, nil
}
