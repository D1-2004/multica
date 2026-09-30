package handler

// Connector OAuth callback forwarding between deployments
// (docs/internal-mcp-connectors.md "Callback origin and forwarding").
//
// A deployment whose provider callbacks cannot come back to its own origin
// (pre-release: providers and the GitHub App only know the production
// domain) sets MULTICA_CONNECTOR_OAUTH_CALLBACK_ORIGIN to the production
// origin. Its states then name the deployment the connect belongs to:
//
//	mcpc.<43 base64url random>.<base64url(home origin)>
//
// The full state is still what the home deployment hashes and looks up, and
// the "mcpc." prefix still routes GitHub callbacks to the connector flow.
//
// The callback deployment (production) lists the origins it forwards to in
// MULTICA_CONNECTOR_OAUTH_FORWARD_ORIGINS. Before any local handling, both
// callback routes (/api/connector-oauth/callback and the "mcpc." branch of
// /api/github/authorize) send a callback whose state names another listed
// origin to that origin with the same path and raw query (302); a state
// naming an origin that is neither this deployment's nor listed gets the
// invalid-connection page (400). Origins match exactly and only https
// origins are forwarded to, so the forwarder is no open redirect. The home
// deployment completes the forwarded callback in the browser that started
// it: the binding cookie was set on its own origin by the start response.
//
// This file is self-contained so production can ship the forwarder before
// pre-release starts minting such states.

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const (
	// connectorOAuthForwardOriginsEnv lists the https origins whose
	// connector OAuth callbacks this deployment forwards (comma separated).
	connectorOAuthForwardOriginsEnv = "MULTICA_CONNECTOR_OAUTH_FORWARD_ORIGINS"
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

// connectorOAuthForwardOrigins returns the https origins listed in
// MULTICA_CONNECTOR_OAUTH_FORWARD_ORIGINS, canonical; other entries are
// ignored.
func connectorOAuthForwardOrigins() []string {
	var out []string
	for _, entry := range strings.Split(os.Getenv(connectorOAuthForwardOriginsEnv), ",") {
		if origin, ok := normalizeConnectorOAuthOrigin(entry); ok && strings.HasPrefix(origin, "https://") {
			out = append(out, origin)
		}
	}
	return out
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
// to that deployment's same callback path with the same raw query when its
// origin is listed in MULTICA_CONNECTOR_OAUTH_FORWARD_ORIGINS, else the
// invalid-connection page. States without a home origin, naming this
// deployment, or malformed are left to local handling (which refuses the
// malformed ones).
func (h *Handler) forwardConnectorOAuthCallback(w http.ResponseWriter, r *http.Request, via string) bool {
	home, ok := splitConnectorOAuthState(r.URL.Query().Get("state"))
	if !ok || home == "" || h.connectorOAuthOwnOrigin(home) {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	for _, allowed := range connectorOAuthForwardOrigins() {
		if allowed == home {
			_, path := h.connectorOAuthCallbackTarget(via)
			target := home + path
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusFound)
			return true
		}
	}
	h.writeConnectorOAuthInvalidPage(w)
	return true
}
