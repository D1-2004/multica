package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// This file must stay identical on the forwarder-only branch (cut from
// develop) and the connector flow branch.

const connectorOAuthForwardTestSecret = "connector-oauth-forward-test-secret-0123456789"

// forwardRedis keeps connector OAuth forward registrations in memory (the
// two Lua scripts of internal_connector_oauth_forward.go); any other script
// goes to next.
type forwardRedis struct {
	mu   sync.Mutex
	vals map[string]string
	next internalConnectorRedis
}

func (f *forwardRedis) Eval(ctx context.Context, script string, keys []string, args ...interface{}) *redis.Cmd {
	cmd := redis.NewCmd(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch script {
	case connectorOAuthForwardSetScript:
		f.vals[keys[0]] = args[0].(string)
		cmd.SetVal("OK")
	case connectorOAuthForwardTakeScript:
		value, ok := f.vals[keys[0]]
		if !ok {
			cmd.SetErr(redis.Nil)
			return cmd
		}
		delete(f.vals, keys[0])
		cmd.SetVal(value)
	default:
		if f.next == nil {
			cmd.SetErr(redis.Nil)
			return cmd
		}
		return f.next.Eval(ctx, script, keys, args...)
	}
	return cmd
}

// forwardRoundTripper serves registration requests with the production
// handler in process.
type forwardRoundTripper struct{ prod *Handler }

func (rt forwardRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	if req.URL.Path == ConnectorOAuthForwardRegistrationPath {
		rt.prod.HandleConnectorOAuthForwardRegistration(rec, req)
	} else {
		rec.WriteHeader(http.StatusNotFound)
	}
	return rec.Result(), nil
}

// useConnectorOAuthForward makes prod keep registrations (shared secret and
// an in-memory Redis) and routes the pre-release registration client to it.
// A pre-release handler needs the same secret (cfg.A2AForwardRegistrationSecret).
func useConnectorOAuthForward(t *testing.T, prod *Handler) {
	t.Helper()
	prod.cfg.A2AForwardRegistrationSecret = connectorOAuthForwardTestSecret
	prod.InternalConnectorRedis = &forwardRedis{vals: map[string]string{}, next: prod.InternalConnectorRedis}
	previous := connectorOAuthForwardHTTPClient
	connectorOAuthForwardHTTPClient = &http.Client{Transport: forwardRoundTripper{prod: prod}}
	t.Cleanup(func() { connectorOAuthForwardHTTPClient = previous })
}

// Production forwards a pre-release connect's callback (on either callback
// route) back to that pre-release with the same path and query only when the
// pre-release registered the connect, once; everything else naming a
// foreign origin is refused, and GitHub App install callbacks are untouched.
func TestConnectorOAuthForwarderRoutes(t *testing.T) {
	const (
		prodOrigin = "https://forward-prod.example.test"
		preOrigin  = "https://pre-forward-prod.example.test"
	)
	stateOf := func(random, home string) string {
		return connectorOAuthStatePrefix + strings.Repeat(random, 43) + "." + base64.RawURLEncoding.EncodeToString([]byte(home))
	}
	prod := &Handler{cfg: Config{AppURL: prodOrigin, FrontendOrigin: prodOrigin}}
	useConnectorOAuthForward(t, prod)
	pre := &Handler{cfg: Config{AppURL: preOrigin, FrontendOrigin: preOrigin, A2AForwardRegistrationSecret: connectorOAuthForwardTestSecret}}
	registration := func(home string) connectorOAuthForwardRegistration {
		return connectorOAuthForwardRegistration{
			HomeOrigin: home, WorkspaceID: "11111111-1111-4111-8111-111111111111", AgentID: "22222222-2222-4222-8222-222222222222",
			ConnectorID: "33333333-3333-4333-8333-333333333333", ScopeType: "person", ExpiresAtMs: time.Now().Add(10 * time.Minute).UnixMilli(),
		}
	}
	routes := map[string]http.HandlerFunc{
		connectorOAuthCallbackPath:   prod.ConnectorOAuthCallback,
		connectorOAuthGitHubCallback: prod.GitHubAuthorizeCallback,
	}
	callback := func(serve http.HandlerFunc, path, rawQuery string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		serve(rec, httptest.NewRequest(http.MethodGet, path+"?"+rawQuery, nil))
		return rec
	}
	refused := func(rec *httptest.ResponseRecorder) bool {
		return rec.Code == http.StatusBadRequest && rec.Header().Get("Location") == "" && strings.Contains(rec.Body.String(), "连接已失效")
	}
	random := map[string]string{connectorOAuthCallbackPath: "B", connectorOAuthGitHubCallback: "C"}
	for path, serve := range routes {
		state := stateOf(random[path], preOrigin)
		rawQuery := "code=the+code&state=" + url.QueryEscape(state)
		// Not registered: refused even though it names the pre-release.
		if rec := callback(serve, path, rawQuery); !refused(rec) {
			t.Fatalf("%s: unregistered pre-release state = %d %q", path, rec.Code, rec.Header().Get("Location"))
		}
		if err := pre.registerConnectorOAuthForward(context.Background(), prodOrigin, state, registration(preOrigin)); err != nil {
			t.Fatalf("%s: register: %v", path, err)
		}
		rec := callback(serve, path, rawQuery)
		if want := preOrigin + path + "?" + rawQuery; rec.Code != http.StatusFound || rec.Header().Get("Location") != want ||
			rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("%s: forward = %d %q, want %q", path, rec.Code, rec.Header().Get("Location"), want)
		}
		// Single use.
		if rec := callback(serve, path, rawQuery); !refused(rec) {
			t.Fatalf("%s: second forward = %d", path, rec.Code)
		}
		for _, home := range []string{"https://evil.example.test", "https://pre-evil.example.test", "http://pre-forward-prod.example.test"} {
			if rec := callback(serve, path, "code=c&state="+url.QueryEscape(stateOf("D", home))); !refused(rec) {
				t.Fatalf("%s: foreign home %s = %d %q", path, home, rec.Code, rec.Header().Get("Location"))
			}
		}
	}

	// Registration endpoint: only its own pre-release, signed and fresh.
	signed := func(reg connectorOAuthForwardRegistration, secret string, at time.Time) *httptest.ResponseRecorder {
		body, _ := json.Marshal(reg)
		timestamp := strconv.FormatInt(at.UnixMilli(), 10)
		req := httptest.NewRequest(http.MethodPost, ConnectorOAuthForwardRegistrationPath, bytes.NewReader(body))
		req.Header.Set(connectorOAuthForwardTimestampHeader, timestamp)
		req.Header.Set(connectorOAuthForwardSignatureHeader, signAgentA2AForwardRegistration([]byte(secret), timestamp, body))
		rec := httptest.NewRecorder()
		prod.HandleConnectorOAuthForwardRegistration(rec, req)
		return rec
	}
	reg := registration(preOrigin)
	reg.StateSHA256 = connectorOAuthForwardStateSHA256(stateOf("E", preOrigin))
	if rec := signed(reg, "wrong-secret-wrong-secret-wrong-secret!!", time.Now()); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret = %d", rec.Code)
	}
	if rec := signed(reg, connectorOAuthForwardTestSecret, time.Now().Add(-10*time.Minute)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale signature = %d", rec.Code)
	}
	foreign := registration("https://pre-evil.example.test")
	foreign.StateSHA256 = reg.StateSHA256
	if rec := signed(foreign, connectorOAuthForwardTestSecret, time.Now()); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign pre-release = %d", rec.Code)
	}
	tooLong := reg
	tooLong.ExpiresAtMs = time.Now().Add(time.Hour).UnixMilli()
	if rec := signed(tooLong, connectorOAuthForwardTestSecret, time.Now()); rec.Code != http.StatusBadRequest {
		t.Fatalf("expiry beyond the bound = %d", rec.Code)
	}
	if rec := signed(reg, connectorOAuthForwardTestSecret, time.Now()); rec.Code != http.StatusOK {
		t.Fatalf("valid registration = %d %s", rec.Code, rec.Body.String())
	}

	// A pre-release without the shared secret cannot start such a connect,
	// and a deployment without it keeps no registrations.
	if err := (&Handler{cfg: Config{AppURL: preOrigin}}).registerConnectorOAuthForward(context.Background(), prodOrigin, stateOf("F", preOrigin), registration(preOrigin)); err == nil {
		t.Fatal("registration without the shared secret")
	}
	rec := httptest.NewRecorder()
	(&Handler{cfg: Config{AppURL: prodOrigin}}).HandleConnectorOAuthForwardRegistration(rec, httptest.NewRequest(http.MethodPost, ConnectorOAuthForwardRegistrationPath, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("registry without the secret = %d", rec.Code)
	}

	// The legacy DCR path is the same handler. An unregistered state is
	// refused, and a registered one is forwarded to the console-registered
	// path (the path new connects put in redirect_uri and on the cookie).
	legacyState := stateOf("I", preOrigin)
	legacyQuery := "code=the+code&state=" + url.QueryEscape(legacyState)
	if rec := callback(prod.ConnectorOAuthCallback, connectorOAuthCallbackLegacyPath, legacyQuery); !refused(rec) {
		t.Fatalf("legacy unregistered = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if err := pre.registerConnectorOAuthForward(context.Background(), prodOrigin, legacyState, registration(preOrigin)); err != nil {
		t.Fatalf("legacy register: %v", err)
	}
	rec = callback(prod.ConnectorOAuthCallback, connectorOAuthCallbackLegacyPath, legacyQuery)
	if want := preOrigin + connectorOAuthCallbackPath + "?" + legacyQuery; rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
		t.Fatalf("legacy forward = %d %q, want %q", rec.Code, rec.Header().Get("Location"), want)
	}

	// Without the connector flow in the build, a callback of production's
	// own connect gets the invalid-connection page.
	if connectorOAuthCompleteLocal == nil {
		if rec := callback(prod.ConnectorOAuthCallback, connectorOAuthCallbackPath, "code=c&state="+url.QueryEscape(stateOf("G", prodOrigin))); !refused(rec) {
			t.Fatalf("own state without the connector flow = %d", rec.Code)
		}
	}
	// A GitHub App install callback (no "mcpc." prefix) keeps the install
	// flow: an unreadable intent goes back to the settings page.
	rec = callback(prod.GitHubAuthorizeCallback, connectorOAuthGitHubCallback, "code=c&state=install-state")
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "github_error=invalid_state") {
		t.Fatalf("install callback = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if !IsConnectorOAuthCallback(httptest.NewRequest(http.MethodGet, connectorOAuthGitHubCallback+"?state="+url.QueryEscape(stateOf("H", preOrigin)), nil)) ||
		IsConnectorOAuthCallback(httptest.NewRequest(http.MethodGet, connectorOAuthGitHubCallback+"?state=install-state", nil)) {
		t.Fatal("router dispatch of the GitHub callback")
	}
}
