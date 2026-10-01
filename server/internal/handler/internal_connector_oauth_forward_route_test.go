package handler

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The forwarder on its own: production sends a pre-release connect's
// callback (on either callback route) back to its pre-release with the same
// path and query, refuses foreign origins, and leaves GitHub App install
// callbacks alone. This file must stay identical on the forwarder-only
// branch (cut from develop) and the connector flow branch.
func TestConnectorOAuthForwarderRoutes(t *testing.T) {
	const (
		prodOrigin = "https://forward-prod.example.test"
		preOrigin  = "https://pre-forward-prod.example.test"
		random     = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	)
	stateOf := func(home string) string {
		return connectorOAuthStatePrefix + random + "." + base64.RawURLEncoding.EncodeToString([]byte(home))
	}
	prod := &Handler{cfg: Config{AppURL: prodOrigin, FrontendOrigin: prodOrigin}}
	routes := map[string]http.HandlerFunc{
		connectorOAuthCallbackPath:   prod.ConnectorOAuthCallback,
		connectorOAuthGitHubCallback: prod.GitHubAuthorizeCallback,
	}
	for path, serve := range routes {
		rawQuery := "code=the+code&state=" + url.QueryEscape(stateOf(preOrigin))
		rec := httptest.NewRecorder()
		serve(rec, httptest.NewRequest(http.MethodGet, path+"?"+rawQuery, nil))
		if want := preOrigin + path + "?" + rawQuery; rec.Code != http.StatusFound || rec.Header().Get("Location") != want ||
			rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("%s: forward = %d %q, want %q", path, rec.Code, rec.Header().Get("Location"), want)
		}
		for _, home := range []string{"https://evil.example.test", "https://pre-evil.example.test", "http://pre-forward-prod.example.test"} {
			rec := httptest.NewRecorder()
			serve(rec, httptest.NewRequest(http.MethodGet, path+"?code=c&state="+url.QueryEscape(stateOf(home)), nil))
			if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" || !strings.Contains(rec.Body.String(), "连接已失效") {
				t.Fatalf("%s: foreign home %s = %d %q", path, home, rec.Code, rec.Header().Get("Location"))
			}
		}
	}
	// Without the connector flow in the build, a callback of production's
	// own connect gets the invalid-connection page.
	if connectorOAuthCompleteLocal == nil {
		rec := httptest.NewRecorder()
		prod.ConnectorOAuthCallback(rec, httptest.NewRequest(http.MethodGet, connectorOAuthCallbackPath+"?code=c&state="+url.QueryEscape(stateOf(prodOrigin)), nil))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "连接已失效") {
			t.Fatalf("own state without the connector flow = %d", rec.Code)
		}
	}
	// A GitHub App install callback (no "mcpc." prefix) keeps the install
	// flow: an unreadable intent goes back to the settings page.
	rec := httptest.NewRecorder()
	prod.GitHubAuthorizeCallback(rec, httptest.NewRequest(http.MethodGet, connectorOAuthGitHubCallback+"?code=c&state=install-state", nil))
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "github_error=invalid_state") {
		t.Fatalf("install callback = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if !IsConnectorOAuthCallback(httptest.NewRequest(http.MethodGet, connectorOAuthGitHubCallback+"?state="+url.QueryEscape(stateOf(preOrigin)), nil)) ||
		IsConnectorOAuthCallback(httptest.NewRequest(http.MethodGet, connectorOAuthGitHubCallback+"?state=install-state", nil)) {
		t.Fatal("router dispatch of the GitHub callback")
	}
}
