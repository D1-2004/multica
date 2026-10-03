package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type githubEnvRedis struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (g *githubEnvRedis) Eval(ctx context.Context, script string, keys []string, args ...interface{}) *redis.Cmd {
	cmd := redis.NewCmd(ctx)
	if script != githubEnvForwardTakeScript || len(keys) != 1 {
		cmd.SetErr(redis.Nil)
		return cmd
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.seen[keys[0]] {
		cmd.SetVal(int64(0))
		return cmd
	}
	g.seen[keys[0]] = true
	cmd.SetVal(int64(1))
	return cmd
}

func githubEnvProdHandler() (*Handler, *githubEnvRedis) {
	store := &githubEnvRedis{seen: map[string]bool{}}
	h := &Handler{cfg: Config{
		FrontendOrigin:               "https://fde-workbench.dingtalk.com",
		A2AForwardRegistrationSecret: connectorOAuthForwardTestSecret,
	}, InternalConnectorRedis: store}
	return h, store
}

func githubEnvPreCookie(t *testing.T) *http.Cookie {
	t.Helper()
	pre := &Handler{cfg: Config{
		FrontendOrigin:               "https://pre-fde-workbench.dingtalk.com",
		A2AForwardRegistrationSecret: connectorOAuthForwardTestSecret,
	}}
	cookie := pre.githubEnvForwardCookie("https://pre-fde-workbench.dingtalk.com")
	if cookie == nil {
		t.Fatal("pre-release did not mint the env cookie")
	}
	return cookie
}

func TestGitHubEnvForwardCookieShape(t *testing.T) {
	cookie := githubEnvPreCookie(t)
	if cookie.Name != githubEnvForwardCookieName || cookie.Domain != githubEnvForwardDomain ||
		cookie.Path != githubEnvForwardCookiePath || !cookie.HttpOnly || !cookie.Secure ||
		cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != int(githubEnvForwardTTL.Seconds()) {
		t.Fatalf("cookie shape = %#v", cookie)
	}
	if !strings.HasPrefix(cookie.Value, "pre.") {
		t.Fatalf("value = %q, want pre.<exp>.<nonce>.<mac>", cookie.Value)
	}
	header := cookie.String()
	// net/http writes the parent domain without the obsolete leading dot.
	// Browsers still send it to every dingtalk.com host.
	for _, part := range []string{"Domain=dingtalk.com", "HttpOnly", "Secure", "SameSite=Lax", "Path=/api/github"} {
		if !strings.Contains(header, part) {
			t.Fatalf("Set-Cookie %q missing %q", header, part)
		}
	}
	prod, _ := githubEnvProdHandler()
	if got := prod.githubEnvForwardCookie("https://fde-workbench.dingtalk.com"); got != nil {
		t.Fatalf("production minted a forward cookie: %#v", got)
	}
	foreign := &Handler{cfg: Config{
		FrontendOrigin:               "https://pre-prod.example.test",
		A2AForwardRegistrationSecret: connectorOAuthForwardTestSecret,
	}}
	if got := foreign.githubEnvForwardCookie("https://pre-prod.example.test"); got != nil {
		t.Fatalf("non-dingtalk origin minted a .dingtalk.com cookie: %#v", got)
	}
}

func TestGitHubSetupForwardsQueryWhenEnvCookieValid(t *testing.T) {
	h, _ := githubEnvProdHandler()
	query := "installation_id=167470537&setup_action=install"
	req := httptest.NewRequest(http.MethodGet, "/api/github/setup?"+query, nil)
	req.AddCookie(githubEnvPreCookie(t))
	rec := httptest.NewRecorder()
	h.GitHubSetupCallback(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	want := "https://pre-fde-workbench.dingtalk.com/api/github/setup?" + query
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
	cleared := false
	for _, line := range rec.Header().Values("Set-Cookie") {
		if strings.Contains(line, githubEnvForwardCookieName+"=") && strings.Contains(line, "Max-Age=0") &&
			strings.Contains(line, "Domain=dingtalk.com") && strings.Contains(line, "Path=/api/github") {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("forward did not clear the cookie: %v", rec.Header().Values("Set-Cookie"))
	}
}

func TestGitHubSetupWithoutEnvCookieStaysOnProduction(t *testing.T) {
	h, _ := githubEnvProdHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/github/setup?installation_id=167470537&setup_action=install", nil)
	rec := httptest.NewRecorder()
	h.GitHubSetupCallback(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "https://fde-workbench.dingtalk.com/settings") || !strings.Contains(loc, "github_error=missing_params") {
		t.Fatalf("Location = %q, want production missing_params", loc)
	}
	if strings.Contains(loc, "pre-fde-workbench") {
		t.Fatalf("production callback was forwarded: %s", loc)
	}
	forged := githubEnvPreCookie(t)
	forged.Value = "pre.1.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	req = httptest.NewRequest(http.MethodGet, "/api/github/setup", nil)
	req.AddCookie(forged)
	rec = httptest.NewRecorder()
	h.GitHubSetupCallback(rec, req)
	loc = rec.Header().Get("Location")
	if strings.Contains(loc, "pre-fde-workbench.dingtalk.com/api/github/setup") {
		t.Fatalf("bad signature was forwarded: %s", loc)
	}
}

func TestGitHubEnvCookieIsSingleUse(t *testing.T) {
	h, _ := githubEnvProdHandler()
	cookie := githubEnvPreCookie(t)
	query := "installation_id=167470537"
	first := httptest.NewRequest(http.MethodGet, "/api/github/setup?"+query, nil)
	first.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.GitHubSetupCallback(rec, first)
	if rec.Header().Get("Location") != "https://pre-fde-workbench.dingtalk.com/api/github/setup?"+query {
		t.Fatalf("first Location = %q", rec.Header().Get("Location"))
	}
	second := httptest.NewRequest(http.MethodGet, "/api/github/authorize?code=abc&state=mcpc.replay", nil)
	second.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.GitHubAuthorizeCallback(rec, second)
	loc := rec.Header().Get("Location")
	if strings.Contains(loc, "pre-fde-workbench.dingtalk.com/api/github/authorize") {
		t.Fatalf("replayed cookie was forwarded: %s", loc)
	}
}

func TestGitHubPreOriginDoesNotForwardEnvCookie(t *testing.T) {
	pre := &Handler{cfg: Config{
		FrontendOrigin:               "https://pre-fde-workbench.dingtalk.com",
		A2AForwardRegistrationSecret: connectorOAuthForwardTestSecret,
	}, InternalConnectorRedis: &githubEnvRedis{seen: map[string]bool{}}}
	req := httptest.NewRequest(http.MethodGet, "/api/github/setup?installation_id=167470537&setup_action=install", nil)
	req.AddCookie(githubEnvPreCookie(t))
	rec := httptest.NewRecorder()
	pre.GitHubSetupCallback(rec, req)
	loc := rec.Header().Get("Location")
	if strings.Contains(loc, "/api/github/setup") {
		t.Fatalf("pre-release forwarded to itself: %s", loc)
	}
	if !strings.Contains(loc, "github_error=missing_params") {
		t.Fatalf("Location = %q, want the local setup fallback", loc)
	}
	prod, _ := githubEnvProdHandler()
	again := httptest.NewRequest(http.MethodGet, "/api/github/setup?installation_id=167470537&setup_action=install", nil)
	again.AddCookie(req.Cookies()[0])
	rec = httptest.NewRecorder()
	prod.GitHubSetupCallback(rec, again)
	if rec.Header().Get("Location") != "https://pre-fde-workbench.dingtalk.com/api/github/setup?installation_id=167470537&setup_action=install" {
		t.Fatalf("pre-release consumed the nonce; production Location = %q", rec.Header().Get("Location"))
	}
}

func TestGitHubEnvCookieExpiredDoesNotForward(t *testing.T) {
	h, _ := githubEnvProdHandler()
	value := formatGitHubEnvForward(h.agentA2AForwardSecret(), time.Now().Add(-2*time.Minute).Unix(), "0123456789abcdef0123456789abcdef")
	req := httptest.NewRequest(http.MethodGet, "/api/github/setup?installation_id=1", nil)
	req.AddCookie(&http.Cookie{Name: githubEnvForwardCookieName, Value: value})
	rec := httptest.NewRecorder()
	h.GitHubSetupCallback(rec, req)
	loc := rec.Header().Get("Location")
	if strings.Contains(loc, "/api/github/setup") {
		t.Fatalf("expired cookie was forwarded: %s", loc)
	}
	if !strings.Contains(loc, "github_error=missing_params") {
		t.Fatalf("Location = %q, want the production fallback", loc)
	}
}

func TestGitHubAuthorizeForwardsWhenEnvCookieValid(t *testing.T) {
	h, _ := githubEnvProdHandler()
	query := "code=abc&state=mcpc.keep-this&setup_action=install"
	req := httptest.NewRequest(http.MethodGet, "/api/github/authorize?"+query, nil)
	req.AddCookie(githubEnvPreCookie(t))
	rec := httptest.NewRecorder()
	h.GitHubAuthorizeCallback(rec, req)
	want := "https://pre-fde-workbench.dingtalk.com/api/github/authorize?" + query
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("status %d Location %q, want %q", rec.Code, rec.Header().Get("Location"), want)
	}
}
