package handler

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// githubEnvForwardCookieName is the short-lived signal a pre-release
	// GitHub App install sets on Domain=.dingtalk.com. Production's setup
	// URL does not receive state, so this cookie is how production tells a
	// pre-release install apart from its own.
	githubEnvForwardCookieName = "multica_gh_env"
	githubEnvForwardCookiePath = "/api/github"
	// githubEnvForwardDomain is the parent domain. net/http emits it as
	// Domain=dingtalk.com (no leading dot); browsers send that cookie to
	// fde-workbench and pre-fde-workbench alike.
	githubEnvForwardDomain    = ".dingtalk.com"
	githubEnvForwardEnv       = "pre"
	githubEnvForwardTTL       = 10 * time.Minute
	githubEnvForwardSkew      = time.Minute
	githubEnvForwardKeyPrefix = "github_env_forward:"
)

// githubEnvForwardTakeScript consumes a nonce once. ARGV[1] is the TTL in
// milliseconds. 1 means this caller owns it; 0 means it was already used.
const githubEnvForwardTakeScript = `if redis.call('SET', KEYS[1], '1', 'NX', 'PX', ARGV[1]) then return 1 else return 0 end`

// githubEnvForwardCookie is the cookie a pre-release sets when a GitHub App
// install starts. Production deployments and origins outside dingtalk.com
// get nothing: the cookie would otherwise send their own callbacks to pre.
func (h *Handler) githubEnvForwardCookie(origin string) *http.Cookie {
	if _, pre := connectorOAuthProductionOrigin(h.githubFrontend()); !pre {
		return nil
	}
	if !githubEnvForwardDomainAllowed(origin) {
		return nil
	}
	secret := h.agentA2AForwardSecret()
	if secret == nil {
		return nil
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil
	}
	exp := time.Now().Add(githubEnvForwardTTL).Unix()
	nonce := hex.EncodeToString(raw[:])
	return &http.Cookie{
		Name: githubEnvForwardCookieName, Value: formatGitHubEnvForward(secret, exp, nonce),
		Domain: githubEnvForwardDomain, Path: githubEnvForwardCookiePath,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int(githubEnvForwardTTL.Seconds()),
	}
}

func clearGitHubEnvForwardCookie() *http.Cookie {
	return &http.Cookie{
		Name: githubEnvForwardCookieName, Value: "",
		Domain: githubEnvForwardDomain, Path: githubEnvForwardCookiePath,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	}
}

// githubEnvForwardDomainAllowed reports whether origin may set the shared
// parent-domain cookie. The Domain attribute is fixed at .dingtalk.com.
func githubEnvForwardDomainAllowed(origin string) bool {
	normalized, ok := normalizeConnectorOAuthOrigin(origin)
	if !ok || !strings.HasPrefix(normalized, "https://") {
		return false
	}
	host := strings.TrimPrefix(normalized, "https://")
	if hostname, _, found := strings.Cut(host, ":"); found {
		host = hostname
	}
	return host == "dingtalk.com" || strings.HasSuffix(host, ".dingtalk.com")
}

// githubPreReleaseSibling is the pre-release origin of a production origin
// ("https://host" → "https://pre-host") when that sibling would recognize
// this origin as its production. Anything else, including an origin that is
// already pre-release, is refused.
func githubPreReleaseSibling(productionOrigin string) (string, bool) {
	normalized, ok := normalizeConnectorOAuthOrigin(productionOrigin)
	if !ok || !strings.HasPrefix(normalized, "https://") {
		return "", false
	}
	host := strings.TrimPrefix(normalized, "https://")
	if strings.HasPrefix(host, "pre-") {
		return "", false
	}
	candidate := "https://pre-" + host
	got, pre := connectorOAuthProductionOrigin(candidate)
	if !pre || got != normalized {
		return "", false
	}
	return candidate, true
}

func formatGitHubEnvForward(secret []byte, exp int64, nonce string) string {
	return githubEnvForwardEnv + "." + strconv.FormatInt(exp, 10) + "." + nonce + "." + signGitHubEnvForward(secret, exp, nonce)
}

func signGitHubEnvForward(secret []byte, exp int64, nonce string) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(githubEnvForwardEnv))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(strconv.FormatInt(exp, 10)))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyGitHubEnvForward checks the signature and the 10-minute window.
// fresh is false when the signature is valid but the cookie has expired.
func verifyGitHubEnvForward(secret []byte, value string, now time.Time) (exp int64, nonce string, fresh, ok bool) {
	if len(secret) == 0 || len(value) > 180 {
		return 0, "", false, false
	}
	env, rest, found := strings.Cut(value, ".")
	if !found || env != githubEnvForwardEnv {
		return 0, "", false, false
	}
	expText, rest, found := strings.Cut(rest, ".")
	if !found || expText == "" || len(expText) > 11 {
		return 0, "", false, false
	}
	nonce, macText, found := strings.Cut(rest, ".")
	if !found || len(nonce) != 32 || len(macText) != 64 {
		return 0, "", false, false
	}
	if _, err := hex.DecodeString(nonce); err != nil {
		return 0, "", false, false
	}
	mac, err := hex.DecodeString(macText)
	if err != nil {
		return 0, "", false, false
	}
	exp, err = strconv.ParseInt(expText, 10, 64)
	if err != nil {
		return 0, "", false, false
	}
	expected, err := hex.DecodeString(signGitHubEnvForward(secret, exp, nonce))
	if err != nil || !hmac.Equal(mac, expected) {
		return 0, "", false, false
	}
	expires := time.Unix(exp, 0)
	if expires.Before(now.Add(-githubEnvForwardSkew)) || expires.After(now.Add(githubEnvForwardTTL+githubEnvForwardSkew)) {
		return exp, nonce, false, true
	}
	return exp, nonce, true, true
}

func (h *Handler) takeGitHubEnvForward(ctx context.Context, nonce string, ttl time.Duration) (bool, error) {
	if h == nil || h.InternalConnectorRedis == nil {
		return false, errors.New("github env forward nonce store is not configured")
	}
	if ttl < time.Millisecond {
		ttl = time.Millisecond
	}
	if ttl > githubEnvForwardTTL+githubEnvForwardSkew {
		ttl = githubEnvForwardTTL + githubEnvForwardSkew
	}
	n, err := h.InternalConnectorRedis.Eval(ctx, githubEnvForwardTakeScript,
		[]string{githubEnvForwardKeyPrefix + nonce}, ttl.Milliseconds()).Int64()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// forwardGitHubPreEnvCookie answers GET /api/github/setup and
// /api/github/authorize when the browser carries a still-valid pre-release
// install cookie: one 302 to the same path on this deployment's pre-release,
// with the raw query unchanged, and the cookie cleared. A pre-release
// deployment, a missing or bad cookie, or a nonce already used returns false
// and leaves the existing handler to run. The cookie is not a state; it only
// says this browser started an install on pre.
func (h *Handler) forwardGitHubPreEnvCookie(w http.ResponseWriter, r *http.Request) bool {
	if h == nil || r == nil || r.URL == nil || r.Method != http.MethodGet {
		return false
	}
	if r.URL.Path != "/api/github/setup" && r.URL.Path != "/api/github/authorize" {
		return false
	}
	if strings.ContainsAny(r.URL.RawQuery, "\r\n") {
		return false
	}
	if _, pre := connectorOAuthProductionOrigin(h.githubFrontend()); pre {
		return false
	}
	cookie, err := r.Cookie(githubEnvForwardCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	exp, nonce, fresh, ok := verifyGitHubEnvForward(h.agentA2AForwardSecret(), cookie.Value, time.Now())
	if !ok {
		return false
	}
	if !fresh {
		http.SetCookie(w, clearGitHubEnvForwardCookie())
		return false
	}
	preOrigin, ok := githubPreReleaseSibling(h.githubFrontend())
	if !ok {
		return false
	}
	taken, err := h.takeGitHubEnvForward(r.Context(), nonce, time.Until(time.Unix(exp, 0)))
	if err != nil {
		slog.WarnContext(r.Context(), "github env cookie forward skipped", "event", "github_env_forward_skipped", "error", err)
		return false
	}
	if !taken {
		http.SetCookie(w, clearGitHubEnvForwardCookie())
		return false
	}
	http.SetCookie(w, clearGitHubEnvForwardCookie())
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	target := preOrigin + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	slog.InfoContext(r.Context(), "github env cookie forwarded", "event", "github_env_forwarded", "path", r.URL.Path)
	http.Redirect(w, r, target, http.StatusFound)
	return true
}
