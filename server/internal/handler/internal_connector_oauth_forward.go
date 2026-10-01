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
// Before any local handling, both callback routes of production
// (/api/connector-oauth/callback and the "mcpc." branch of
// /api/github/authorize) send a callback whose state names production's own
// pre-release origin to that origin with the same path and raw query (302);
// a state naming any other foreign origin gets the invalid-connection page
// (400). Only "https://pre-" + an own host below its registrable domain
// qualifies (connectorOAuthProductionOrigin), so the forwarder is no open
// redirect and needs no configuration. The home deployment completes
// the forwarded callback in the browser that started it: the binding cookie
// was set on its own origin by the start response.
//
// This file is self-contained: it holds the callback routes' entry points
// and everything the forwarder needs, so a build without the connector flow
// (a forwarder-only production release cut from develop) compiles it
// unchanged. The connector flow plugs its local completion in through
// connectorOAuthCompleteLocal; without it, a callback of this deployment's
// own state gets the invalid-connection page. Keep this file identical on
// every branch that carries it.

import (
	"encoding/base64"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/publicsuffix"
)

const (
	// connectorOAuthStatePrefix marks connector OAuth states, so the shared
	// GitHub App callback can tell them from GitHub App install states.
	connectorOAuthStatePrefix    = "mcpc."
	connectorOAuthCallbackPath   = "/api/connector-oauth/callback"
	connectorOAuthGitHubCallback = "/api/github/authorize"
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
	_, path := h.connectorOAuthCallbackTarget(via)
	target := home + path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusFound)
	return true
}

const (
	// Callback routes a state may complete on.
	connectorOAuthViaDCR    = "dcr"
	connectorOAuthViaGitHub = "github"
)

// ConnectorOAuthCallbackPath is the public DCR OAuth callback route. The
// router registers it outside the authenticated group.
const ConnectorOAuthCallbackPath = connectorOAuthCallbackPath

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
