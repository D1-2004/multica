package handler

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func installResumeState(t *testing.T) string {
	t.Helper()
	return connectorOAuthStatePrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
}

func TestRestoreGitHubInstallStateFromCookie(t *testing.T) {
	state := installResumeState(t)
	req := httptest.NewRequest(http.MethodGet, "/api/github/setup?installation_id=167470537&setup_action=install", nil)
	req.AddCookie(connectorOAuthActiveInstallCookieValue(state, "https://pre.example"))
	restoreGitHubInstallState(req)
	if got := req.URL.Query().Get("state"); got != state {
		t.Fatalf("state = %q", got)
	}

	// A code stays bound to the query. The cookie must not supply a state.
	withCode := httptest.NewRequest(http.MethodGet, "/api/github/authorize?code=abc&installation_id=167470537", nil)
	withCode.AddCookie(connectorOAuthActiveInstallCookieValue(state, "https://pre.example"))
	restoreGitHubInstallState(withCode)
	if got := withCode.URL.Query().Get("state"); got != "" {
		t.Fatalf("code callback recovered state %q", got)
	}

	// No installation id and no setup action: not a setup redirect.
	bare := httptest.NewRequest(http.MethodGet, "/api/github/authorize", nil)
	bare.AddCookie(connectorOAuthActiveInstallCookieValue(state, "https://pre.example"))
	restoreGitHubInstallState(bare)
	if got := bare.URL.Query().Get("state"); got != "" {
		t.Fatalf("bare callback recovered state %q", got)
	}

	// The workspace install cookie owns an empty state.
	workspace := httptest.NewRequest(http.MethodGet, "/api/github/setup?installation_id=167470537", nil)
	workspace.AddCookie(&http.Cookie{Name: githubConnectCookie, Value: "eyJhbGciOiJIUzI1NiJ9.e30"})
	workspace.AddCookie(connectorOAuthActiveInstallCookieValue(state, "https://pre.example"))
	restoreGitHubInstallState(workspace)
	if got := workspace.URL.Query().Get("state"); got != "" {
		t.Fatalf("workspace callback recovered state %q", got)
	}
}

func TestGitHubSetupCallbackResumesConnectorInstall(t *testing.T) {
	state := installResumeState(t)
	var sawVia, sawState string
	prev := connectorOAuthCompleteLocal
	connectorOAuthCompleteLocal = func(_ *Handler, w http.ResponseWriter, r *http.Request, via string) {
		sawVia = via
		sawState = r.URL.Query().Get("state")
		w.WriteHeader(http.StatusNoContent)
	}
	t.Cleanup(func() { connectorOAuthCompleteLocal = prev })

	h := &Handler{cfg: Config{FrontendOrigin: "https://pre.example"}}
	req := httptest.NewRequest(http.MethodGet, "/api/github/setup?installation_id=167470537", nil)
	req.AddCookie(connectorOAuthActiveInstallCookieValue(state, "https://pre.example"))
	rec := httptest.NewRecorder()
	h.GitHubSetupCallback(rec, req)
	if rec.Code != http.StatusNoContent || sawVia != connectorOAuthViaGitHub || sawState != state {
		t.Fatalf("setup resume = %d via %s state %q location %s", rec.Code, sawVia, sawState, rec.Header().Get("Location"))
	}

	// The same cookie on the authorize URL, still without a code.
	sawVia, sawState = "", ""
	authReq := httptest.NewRequest(http.MethodGet, "/api/github/authorize?installation_id=167470537&setup_action=update", nil)
	authReq.AddCookie(connectorOAuthActiveInstallCookieValue(state, "https://pre.example"))
	authRec := httptest.NewRecorder()
	h.GitHubAuthorizeCallback(authRec, authReq)
	if authRec.Code != http.StatusNoContent || sawVia != connectorOAuthViaGitHub || sawState != state {
		t.Fatalf("authorize resume = %d via %s state %q location %s", authRec.Code, sawVia, sawState, authRec.Header().Get("Location"))
	}
}

func TestGitHubSetupCallbackWithoutResumeStaysOnWorkspaceSettings(t *testing.T) {
	h := &Handler{cfg: Config{FrontendOrigin: "https://pre.example"}}
	req := httptest.NewRequest(http.MethodGet, "/api/github/setup?installation_id=167470537&setup_action=install", nil)
	rec := httptest.NewRecorder()
	h.GitHubSetupCallback(rec, req)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "github_error=missing_params") {
		t.Fatalf("setup without state = %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestGitHubInstallQueryWithoutSetupActionIsInstall(t *testing.T) {
	query := url.Values{"installation_id": {"99"}}
	got := githubConnectorCallbackFromQuery(connectorOAuthViaGitHub, query)
	if got.SetupAction != "install" || got.InstallationID != 99 || got.Code != "" {
		t.Fatalf("callback = %+v", got)
	}
	request := url.Values{"installation_id": {"99"}, "setup_action": {"request"}}
	if got := githubConnectorCallbackFromQuery(connectorOAuthViaGitHub, request); got.SetupAction != "" {
		t.Fatalf("setup_action=request promoted to %q", got.SetupAction)
	}
	withCode := url.Values{"installation_id": {"99"}, "code": {"abc"}}
	if got := githubConnectorCallbackFromQuery(connectorOAuthViaGitHub, withCode); got.SetupAction != "" || got.Code != "abc" {
		t.Fatalf("code callback = %+v", got)
	}
}

func TestActiveInstallCookieShape(t *testing.T) {
	state := installResumeState(t)
	cookie := connectorOAuthActiveInstallCookieValue(state, "https://pre.example")
	if cookie.Name != connectorOAuthActiveInstallCookie || cookie.Path != connectorOAuthActiveInstallPath ||
		!cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Value != state {
		t.Fatalf("cookie = %+v", cookie)
	}
	cleared := connectorOAuthActiveInstallCookieValue("", "https://pre.example")
	if cleared.MaxAge >= 0 || cleared.Path != connectorOAuthActiveInstallPath {
		t.Fatalf("cleared = %+v", cleared)
	}
	if !validConnectorOAuthState(state) {
		t.Fatal("fixture state is not a connector state")
	}
	escaped := url.QueryEscape(state)
	if escaped == "" {
		t.Fatal("state did not encode")
	}
}
