package handler

// Connector OAuth callback forwarding between deployments
// (docs/internal-mcp-connectors.md "Callback origin and forwarding").
//
// A pre-release deployment serves the production host with a "pre-" prefix
// (https://pre-fde-workbench.dingtalk.com is the pre-release of
// https://fde-workbench.dingtalk.com). Its connects send providers the
// production callback, and its states name the deployment the connect
// belongs to:
//
//	mcpc.<43 base64url random>.<base64url(home origin)>
//
// The full state is still what the home deployment hashes and looks up, and
// the "mcpc." prefix still routes GitHub callbacks to the connector flow.
// Every other deployment (production, local) uses its own callback.
//
// Production forwards only connects its pre-release registered, the same
// shape as the A2A forward registrations (agent_a2a_forward_registration.go):
// pre-release signs, production verifies. When a pre-release starts a
// connect it registers it with production before the browser leaves:
// {sha256(state), its origin, workspace, agent, connector, scope, expiry},
// signed with HMAC-SHA256 over the timestamp and body using
// MULTICA_A2A_FORWARD_REGISTRATION_SECRET, which both deployments already
// share (signAgentA2AForwardRegistration). Production keeps the
// registration in Redis until the state expires. On a callback, production's
// callback routes (/api/connector-oauth/callback, the console-registered
// alias /api/connectors/oauth/callback, and the "mcpc." branch of
// /api/github/authorize) take the registration of the state (single use)
// and send the callback to that pre-release. The DCR routes forward to the
// canonical v0 path with the raw query (302); the GitHub route forwards to
// its own path. Anything else naming a foreign origin gets the
// invalid-connection page (400). Only "https://pre-" + an own host below its registrable domain
// can register (connectorOAuthProductionOrigin), so production is no open
// redirect, and a callback no pre-release Workspace/Agent connect started is
// never forwarded. Without the secret or Redis it fails closed: the
// pre-release refuses to start such a connect, and production forwards
// nothing. The home deployment completes the forwarded callback in the
// browser that started it: the binding cookie was set on its own origin by
// the start response.
//
// This file is self-contained: it holds the callback routes' entry points
// and everything the forwarder needs, so a build without the connector flow
// (a forwarder-only production release cut from develop) compiles it
// unchanged. The connector flow plugs its local completion in through
// connectorOAuthCompleteLocal; without it, a callback of this deployment's
// own state gets the invalid-connection page. Keep this file identical on
// every branch that carries it.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/net/publicsuffix"
)

const (
	// connectorOAuthStatePrefix marks connector OAuth states, so the shared
	// GitHub App callback can tell them from GitHub App install states.
	connectorOAuthStatePrefix  = "mcpc."
	connectorOAuthCallbackPath = "/api/connector-oauth/callback"
	// connectorOAuthCallbackAliasPath is the DCR callback registered in
	// provider consoles. It is served by the same handler as
	// connectorOAuthCallbackPath, which stays registered so existing
	// redirect URIs keep working.
	connectorOAuthCallbackAliasPath = "/api/connectors/oauth/callback"
	connectorOAuthGitHubCallback    = "/api/github/authorize"
)

const (
	// connectorOAuthPreReleaseOrigin prefixes the origin of a pre-release
	// deployment: "https://pre-" + the production host.
	connectorOAuthPreReleaseOrigin = "https://pre-"
	// connectorOAuthStateHomeSeparator separates a state's random part from
	// its encoded home origin.
	connectorOAuthStateHomeSeparator = "."
	// connectorOAuthStateRandomLength is the base64url length of the 32
	// random bytes of a state.
	connectorOAuthStateRandomLength = 43
	// connectorOAuthMaxHomeOrigin bounds the home origin a state may carry.
	connectorOAuthMaxHomeOrigin = 256
)

// normalizeConnectorOAuthOrigin returns raw as a canonical origin
// ("scheme://host[:port]", lowercase, no trailing slash), or false when it is
// not an http(s) origin: it must have a host and no user info, path, query
// or fragment.
func normalizeConnectorOAuthOrigin(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > connectorOAuthMaxHomeOrigin || strings.ContainsAny(raw, " \t\r\n\x00\\") {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil || u.Host == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", false
	}
	return scheme + "://" + strings.ToLower(u.Host), true
}

// encodeConnectorOAuthStateHome encodes a canonical origin for a state.
func encodeConnectorOAuthStateHome(origin string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(origin))
}

// splitConnectorOAuthState parses a connector OAuth state minted by
// startConnectorOAuth: the prefix, 43 base64url characters and, optionally,
// the separator and the base64url of a canonical origin (the home
// deployment). home is "" for a state without one; ok is false for anything
// else.
func splitConnectorOAuthState(state string) (home string, ok bool) {
	body, found := strings.CutPrefix(state, connectorOAuthStatePrefix)
	if !found || len(body) < connectorOAuthStateRandomLength {
		return "", false
	}
	random, encodedHome := body[:connectorOAuthStateRandomLength], body[connectorOAuthStateRandomLength:]
	if _, err := base64.RawURLEncoding.DecodeString(random); err != nil {
		return "", false
	}
	if encodedHome == "" {
		return "", true
	}
	encodedHome, found = strings.CutPrefix(encodedHome, connectorOAuthStateHomeSeparator)
	if !found || encodedHome == "" || len(encodedHome) > base64.RawURLEncoding.EncodedLen(connectorOAuthMaxHomeOrigin) {
		return "", false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encodedHome)
	if err != nil {
		return "", false
	}
	origin, valid := normalizeConnectorOAuthOrigin(string(decoded))
	if !valid || origin != string(decoded) {
		return "", false
	}
	return origin, true
}

// validConnectorOAuthState accepts exactly the states startConnectorOAuth
// mints (splitConnectorOAuthState).
func validConnectorOAuthState(state string) bool {
	_, ok := splitConnectorOAuthState(state)
	return ok
}

// connectorOAuthProductionOrigin returns the production origin of a
// pre-release origin ("https://pre-<host>" → "https://<host>"), or false for
// any other origin. The production host must be a subdomain below its
// registrable domain (fde-workbench.dingtalk.com under dingtalk.com), so its
// "pre-" sibling lives in the same operator-owned zone: for an apex host
// (qwentag.com) or one on a shared public suffix (x.github.io) anyone could
// register "pre-" + host, and production would forward codes there.
func connectorOAuthProductionOrigin(origin string) (string, bool) {
	normalized, ok := normalizeConnectorOAuthOrigin(origin)
	if !ok {
		return "", false
	}
	host, found := strings.CutPrefix(normalized, connectorOAuthPreReleaseOrigin)
	if !found {
		return "", false
	}
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}
	registrable, err := publicsuffix.EffectiveTLDPlusOne(hostname)
	if err != nil || registrable == hostname {
		return "", false
	}
	return "https://" + host, true
}

// connectorOAuthOwnOrigin reports whether origin is one of this deployment's
// own browser-facing origins (MULTICA_APP_URL, FRONTEND_ORIGIN, and the
// GitHub App callback origin), where its own states complete.
func (h *Handler) connectorOAuthOwnOrigin(origin string) bool {
	cfg := h.currentConfig()
	for _, own := range []string{cfg.AppURL, cfg.FrontendOrigin, h.githubFrontend()} {
		if normalized, ok := normalizeConnectorOAuthOrigin(own); ok && normalized == origin {
			return true
		}
	}
	return false
}

// forwardConnectorOAuthCallback handles a connector OAuth callback whose
// state belongs to another deployment and reports whether it answered: a 302
// to that deployment's same callback path with the same raw query when it is
// this deployment's pre-release, else the invalid-connection page. States without a home origin, naming this
// deployment, or malformed are left to local handling (which refuses the
// malformed ones).
func (h *Handler) forwardConnectorOAuthCallback(w http.ResponseWriter, r *http.Request, via string) bool {
	home, ok := splitConnectorOAuthState(r.URL.Query().Get("state"))
	if !ok || home == "" || h.connectorOAuthOwnOrigin(home) {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if production, pre := connectorOAuthProductionOrigin(home); !pre || !h.connectorOAuthOwnOrigin(production) {
		h.writeConnectorOAuthInvalidPage(w)
		return true
	}
	// Only a connect the pre-release registered for one of its Workspaces
	// and Agents is forwarded, once.
	registration, err := h.takeConnectorOAuthForward(r.Context(), r.URL.Query().Get("state"))
	if err != nil || registration.HomeOrigin != home {
		slog.WarnContext(r.Context(), "connector OAuth callback forward refused", "event", "connector_oauth_forward_refused",
			"home_origin", home, "via", via, "registered", err == nil, "error", err)
		h.writeConnectorOAuthInvalidPage(w)
		return true
	}
	_, path := h.connectorOAuthCallbackTarget(via)
	target := home + path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	slog.InfoContext(r.Context(), "connector OAuth callback forwarded", "event", "connector_oauth_forwarded",
		"home_origin", home, "via", via, "workspace_id", registration.WorkspaceID, "agent_id", registration.AgentID,
		"connector_id", registration.ConnectorID, "scope_type", registration.ScopeType)
	http.Redirect(w, r, target, http.StatusFound)
	return true
}

const (
	// ConnectorOAuthForwardRegistrationPath is production's endpoint for its
	// pre-release's connect registrations. No Multica session: the
	// shared-secret signature is the credential.
	ConnectorOAuthForwardRegistrationPath = "/api/internal/connector-oauth/forward-registrations"
	connectorOAuthForwardSignatureHeader  = "X-Multica-Connector-OAuth-Forward-Signature"
	connectorOAuthForwardTimestampHeader  = "X-Multica-Connector-OAuth-Forward-Timestamp"
	connectorOAuthForwardMaxBody          = 8 << 10
	connectorOAuthForwardMaxSkew          = 5 * time.Minute
	// connectorOAuthForwardMaxTTL bounds how long production keeps a
	// registration (a state lives 10 minutes).
	connectorOAuthForwardMaxTTL    = 15 * time.Minute
	connectorOAuthForwardKeyPrefix = "connector_oauth_forward:"
)

// connectorOAuthForwardSetScript stores a registration until it expires;
// connectorOAuthForwardTakeScript returns it and deletes it (single use).
const (
	connectorOAuthForwardSetScript  = `return redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])`
	connectorOAuthForwardTakeScript = `local v = redis.call('GET', KEYS[1]) if v then redis.call('DEL', KEYS[1]) end return v`
)

// connectorOAuthForwardHTTPClient sends registrations to production; it
// never follows redirects or uses a proxy.
var connectorOAuthForwardHTTPClient = &http.Client{
	Timeout:   10 * time.Second,
	Transport: &http.Transport{Proxy: nil, TLSHandshakeTimeout: 5 * time.Second},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

var errConnectorOAuthForwardUnavailable = errors.New("connector OAuth forwarding is not configured")

// connectorOAuthForwardRegistration is one pre-release connect production
// may forward the callback of. AgentID is "" for a workspace connect.
type connectorOAuthForwardRegistration struct {
	StateSHA256 string `json:"state_sha256"`
	HomeOrigin  string `json:"home_origin"`
	WorkspaceID string `json:"workspace_id"`
	AgentID     string `json:"agent_id"`
	ConnectorID string `json:"connector_id"`
	ScopeType   string `json:"scope_type"`
	ExpiresAtMs int64  `json:"expires_at_ms"`
}

func connectorOAuthForwardStateSHA256(state string) string {
	digest := sha256.Sum256([]byte(state))
	return hex.EncodeToString(digest[:])
}

func (reg connectorOAuthForwardRegistration) valid(now time.Time) bool {
	if len(reg.StateSHA256) != 64 || reg.ScopeType == "" || len(reg.ScopeType) > 32 {
		return false
	}
	if _, err := hex.DecodeString(reg.StateSHA256); err != nil {
		return false
	}
	if _, err := parseStrictUUID(reg.WorkspaceID); err != nil {
		return false
	}
	if _, err := parseStrictUUID(reg.ConnectorID); err != nil {
		return false
	}
	if reg.AgentID != "" {
		if _, err := parseStrictUUID(reg.AgentID); err != nil {
			return false
		}
	}
	expires := time.UnixMilli(reg.ExpiresAtMs)
	return expires.After(now) && !expires.After(now.Add(connectorOAuthForwardMaxTTL))
}

// registerConnectorOAuthForward registers a connect of this pre-release with
// its production (the redirect origin), before the browser goes to the
// provider. It fails closed: without the shared secret or a 200 from
// production the connect must not start, because its callback would not be
// forwarded.
func (h *Handler) registerConnectorOAuthForward(ctx context.Context, production, state string, reg connectorOAuthForwardRegistration) error {
	secret := h.agentA2AForwardSecret()
	if secret == nil {
		return errConnectorOAuthForwardUnavailable
	}
	reg.StateSHA256 = connectorOAuthForwardStateSHA256(state)
	body, err := json.Marshal(reg)
	if err != nil {
		return err
	}
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, production+ConnectorOAuthForwardRegistrationPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(connectorOAuthForwardTimestampHeader, timestamp)
	req.Header.Set(connectorOAuthForwardSignatureHeader, signAgentA2AForwardRegistration(secret, timestamp, body))
	resp, err := connectorOAuthForwardHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("production answered the connect registration with %d", resp.StatusCode)
	}
	return nil
}

// HandleConnectorOAuthForwardRegistration is production's registration
// endpoint: a signed registration of its own pre-release origin is kept
// until the state expires. 404 when this deployment cannot keep
// registrations (no shared secret or Redis).
func (h *Handler) HandleConnectorOAuthForwardRegistration(w http.ResponseWriter, r *http.Request) {
	secret := h.agentA2AForwardSecret()
	if secret == nil || h.InternalConnectorRedis == nil {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, connectorOAuthForwardMaxBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid registration body")
		return
	}
	timestamp := strings.TrimSpace(r.Header.Get(connectorOAuthForwardTimestampHeader))
	signedAtMs, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || absDuration(time.Since(time.UnixMilli(signedAtMs))) > connectorOAuthForwardMaxSkew ||
		!hmac.Equal([]byte(signAgentA2AForwardRegistration(secret, timestamp, body)), []byte(strings.TrimSpace(r.Header.Get(connectorOAuthForwardSignatureHeader)))) {
		writeError(w, http.StatusUnauthorized, "invalid registration signature")
		return
	}
	var reg connectorOAuthForwardRegistration
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid registration body")
		return
	}
	now := time.Now()
	home, ok := normalizeConnectorOAuthOrigin(reg.HomeOrigin)
	if !ok || home != reg.HomeOrigin || !reg.valid(now) {
		writeError(w, http.StatusBadRequest, "invalid registration")
		return
	}
	if production, pre := connectorOAuthProductionOrigin(home); !pre || !h.connectorOAuthOwnOrigin(production) {
		writeError(w, http.StatusForbidden, "not this deployment's pre-release")
		return
	}
	ttl := time.UnixMilli(reg.ExpiresAtMs).Sub(now)
	if err := h.InternalConnectorRedis.Eval(r.Context(), connectorOAuthForwardSetScript,
		[]string{connectorOAuthForwardKeyPrefix + reg.StateSHA256}, string(body), ttl.Milliseconds()).Err(); err != nil {
		slog.ErrorContext(r.Context(), "connector OAuth forward registration not stored", "error", err)
		writeError(w, http.StatusServiceUnavailable, "registration not stored")
		return
	}
	slog.InfoContext(r.Context(), "connector OAuth forward registered", "event", "connector_oauth_forward_registered",
		"home_origin", home, "workspace_id", reg.WorkspaceID, "agent_id", reg.AgentID, "connector_id", reg.ConnectorID, "scope_type", reg.ScopeType)
	writeJSON(w, http.StatusOK, map[string]string{"status": "registered"})
}

// takeConnectorOAuthForward returns and removes the registration of state.
func (h *Handler) takeConnectorOAuthForward(ctx context.Context, state string) (connectorOAuthForwardRegistration, error) {
	var reg connectorOAuthForwardRegistration
	if h.InternalConnectorRedis == nil {
		return reg, errConnectorOAuthForwardUnavailable
	}
	value, err := h.InternalConnectorRedis.Eval(ctx, connectorOAuthForwardTakeScript,
		[]string{connectorOAuthForwardKeyPrefix + connectorOAuthForwardStateSHA256(state)}).Text()
	if errors.Is(err, redis.Nil) {
		return reg, errors.New("no registration for this state")
	}
	if err != nil {
		return reg, err
	}
	if err := json.Unmarshal([]byte(value), &reg); err != nil {
		return reg, err
	}
	if reg.StateSHA256 != connectorOAuthForwardStateSHA256(state) || time.Now().After(time.UnixMilli(reg.ExpiresAtMs)) {
		return reg, errors.New("registration does not match this state")
	}
	return reg, nil
}

const (
	// Callback routes a state may complete on.
	connectorOAuthViaDCR    = "dcr"
	connectorOAuthViaGitHub = "github"
)

// ConnectorOAuthCallbackPath is the public v0 DCR OAuth callback route. The
// router registers it outside the authenticated group.
const ConnectorOAuthCallbackPath = connectorOAuthCallbackPath

// ConnectorOAuthCallbackAliasPath is the console-registered DCR callback.
// The router registers it beside ConnectorOAuthCallbackPath; both call
// ConnectorOAuthCallback.
const ConnectorOAuthCallbackAliasPath = connectorOAuthCallbackAliasPath

// IsConnectorOAuthCallback reports whether a GitHub App callback request
// completes an official app connect (a "mcpc." state), so the router can
// rate-limit those like the DCR callback without touching the install flow.
func IsConnectorOAuthCallback(r *http.Request) bool {
	return isConnectorOAuthState(r.URL.Query().Get("state"))
}

// isConnectorOAuthState reports whether a GitHub callback state belongs to
// the connector flow (GitHubAuthorizeCallback dispatches on it).
func isConnectorOAuthState(state string) bool {
	return strings.HasPrefix(state, connectorOAuthStatePrefix)
}

// connectorOAuthAppOrigin is the browser-facing origin OAuth callbacks and
// return pages live on ("" when none is configured).
func (h *Handler) connectorOAuthAppOrigin() string {
	return strings.TrimRight(firstNonEmpty(h.currentConfig().AppURL, h.currentConfig().FrontendOrigin), "/")
}

// connectorOAuthCallbackTarget returns this deployment's own origin and the
// path of the callback of a connect of the given route: the GitHub App
// callback on FRONTEND_ORIGIN, or the DCR callback on the app origin. The
// start response is served there, so the browser binding cookie lives there,
// and a callback production forwards to a pre-release lands there.
func (h *Handler) connectorOAuthCallbackTarget(via string) (string, string) {
	if via == connectorOAuthViaGitHub {
		return h.githubFrontend(), connectorOAuthGitHubCallback
	}
	return h.connectorOAuthAppOrigin(), connectorOAuthCallbackPath
}

// ConnectorOAuthCallback is the provider redirect of DCR official apps.
func (h *Handler) ConnectorOAuthCallback(w http.ResponseWriter, r *http.Request) {
	h.serveConnectorOAuthCallback(w, r, connectorOAuthViaDCR)
}

// connectorOAuthCompleteLocal completes a callback whose state belongs to
// this deployment. The connector flow sets it at init
// (internal_connector_catalog_api.go); a build without the flow leaves it
// nil.
var connectorOAuthCompleteLocal func(h *Handler, w http.ResponseWriter, r *http.Request, via string)

// serveConnectorOAuthCallback handles a provider redirect on either callback
// route: a callback of another deployment's connect (its state names that
// deployment) is forwarded there or refused before any local handling;
// anything else is completed locally, or gets the invalid-connection page
// when this build has no connector flow.
func (h *Handler) serveConnectorOAuthCallback(w http.ResponseWriter, r *http.Request, via string) {
	if h.forwardConnectorOAuthCallback(w, r, via) {
		return
	}
	if connectorOAuthCompleteLocal == nil {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		h.writeConnectorOAuthInvalidPage(w)
		return
	}
	connectorOAuthCompleteLocal(h, w, r, via)
}

// writeConnectorOAuthInvalidPage answers an unknown, expired or replayed
// connect callback with a small same-origin page instead of a
// JSON body: the browser, often the DingTalk WebView, shows the response
// directly. It links to the app's pages; nothing in it comes from the
// request.
func (h *Handler) writeConnectorOAuthInvalidPage(w http.ResponseWriter) {
	origin := html.EscapeString(h.connectorOAuthAppOrigin())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = io.WriteString(w, `<!doctype html>
<html lang="zh-CN">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>连接已失效</title></head>
<body style="font-family:system-ui,-apple-system,sans-serif;max-width:32rem;margin:12vh auto;padding:0 16px;line-height:1.6;color:#1f2328;background:#fff">
<h1 style="font-size:1.25rem;margin:0 0 .5rem">连接已失效</h1>
<p style="margin:0 0 .5rem">这次连接已过期或已被使用。请回到原页面重新连接。</p>
<p lang="en" style="margin:0 0 1.5rem;color:#59636e">This connection attempt is invalid or has expired. Go back and connect again.</p>
<p style="margin:0"><a href="`+origin+`/dingtalk/configure">返回连接配置页</a> · <a href="`+origin+`/">返回工作台</a></p>
</body>
</html>
`)
}
