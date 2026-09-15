package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func testGitHubConnectIntent(t *testing.T) (githubConnectIntent,string) {
	t.Helper()
	t.Setenv("GITHUB_WEBHOOK_SECRET","connect-test-secret")
	t.Setenv("GITHUB_APP_SLUG","test-app")
	intent := githubConnectIntent{WorkspaceID:testWorkspaceID,UserID:testUserID,ReturnTo:githubReturnToRepositories,RegisteredClaims:jwt.RegisteredClaims{Issuer:"multica-github-connect",ID:uuid.NewString(),ExpiresAt:jwt.NewNumericDate(time.Now().Add(githubConnectTTL))}}
	value,err := signGitHubConnectIntent(intent)
	if err != nil { t.Fatal(err) }
	return intent,value
}

func TestGitHubInstallStartPreservesBrowserContext(t *testing.T) {
	_,value := testGitHubConnectIntent(t)
	h := *testHandler
	h.cfg.FrontendOrigin = "https://app.example"
	rec := httptest.NewRecorder()
	h.GitHubInstallStart(rec,httptest.NewRequest(http.MethodGet,"/api/github/install?state="+url.QueryEscape(value),nil))
	location,err := url.Parse(rec.Header().Get("Location"))
	if err != nil || location.Host != "github.com" || location.Query().Get("state") != value { t.Fatalf("invalid install redirect: %v",err) }
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value != value || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Path != "/api/github" || cookies[0].MaxAge != 900 { t.Fatal("browser context cookie lacks required protection") }
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" { t.Fatal("connection context can leak through browser caching/referrers") }
	_,other := testGitHubConnectIntent(t)
	req := httptest.NewRequest(http.MethodGet,"/api/github/install?state="+url.QueryEscape(other),nil)
	req.AddCookie(cookies[0])
	rec = httptest.NewRecorder(); h.GitHubInstallStart(rec,req)
	if !strings.Contains(rec.Header().Get("Location"),"github_error=connection_in_progress") || rec.Result().Cookies()[0].MaxAge != -1 { t.Fatal("concurrent browser attempts may bind the wrong workspace") }
}

func TestGitHubSetupRejectsInvalidBrowserContext(t *testing.T) {
	intent,valid := testGitHubConnectIntent(t)
	intent.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
	expired,err := signGitHubConnectIntent(intent); if err != nil { t.Fatal(err) }
	for _,tc := range []struct { name,state,cookie string }{
		{"expired","",expired},
		{"tampered","",valid+"x"},
		{"different state",valid+"x",valid},
		{"missing cookie",valid,""},
	} {
		t.Run(tc.name,func(t *testing.T){
			req := httptest.NewRequest(http.MethodGet,"/api/github/setup?installation_id=981901&state="+url.QueryEscape(tc.state),nil)
			if tc.cookie != "" { req.AddCookie(&http.Cookie{Name:githubConnectCookie,Value:tc.cookie}) }
			rec := httptest.NewRecorder(); testHandler.GitHubSetupCallback(rec,req)
			if !strings.Contains(rec.Header().Get("Location"),"github_error=invalid_state") { t.Fatal("invalid browser context accepted") }
		})
	}
}

func TestGitHubAuthorizationRejectsUnboundCallbacks(t *testing.T) {
	intent,_ := testGitHubConnectIntent(t)
	intent.InstallationID = 981901
	value,err := signGitHubConnectIntent(intent); if err != nil { t.Fatal(err) }
	for _,cookie := range []string{"",value+"x"} {
		req := httptest.NewRequest(http.MethodGet,"/api/github/authorize?code=test-code&state="+url.QueryEscape(value),nil)
		if cookie != "" { req.AddCookie(&http.Cookie{Name:githubConnectCookie,Value:cookie}) }
		rec := httptest.NewRecorder()
		// No database or provider is available: invalid callbacks must return
		// before either is consulted, even with a valid signed state.
		(&Handler{}).GitHubAuthorizeCallback(rec,req)
		if !strings.Contains(rec.Header().Get("Location"),"github_error=invalid_state") { t.Fatal("unbound authorization callback accepted") }
	}
}

func TestGitHubReconnectVerifiesUserAndWorkspaceBeforeWriting(t *testing.T) {
	for _,tc := range []struct { name string; withState, denied, suspended, revoked, noConfig, badExchange bool }{
		{name:"authorized"}, {name:"state preserved",withState:true}, {name:"unrelated installation",denied:true}, {name:"suspended",suspended:true},
		{name:"workspace role revoked",revoked:true}, {name:"OAuth not configured",noConfig:true}, {name:"code exchange rejected",badExchange:true},
	} {
		t.Run(tc.name,func(t *testing.T){
			_,value := testGitHubConnectIntent(t)
			t.Setenv("GITHUB_APP_CLIENT_ID","test-client")
			t.Setenv("GITHUB_APP_CLIENT_SECRET","test-client-secret")
			if tc.noConfig { t.Setenv("GITHUB_APP_CLIENT_SECRET","") }
			const installationID int64 = 981901
			_,err := testPool.Exec(t.Context(),`DELETE FROM github_installation WHERE workspace_id=$1 AND installation_id=$2`,testWorkspaceID,installationID); if err != nil { t.Fatal(err) }
			t.Cleanup(func(){ _,_ = testPool.Exec(context.Background(),`DELETE FROM github_installation WHERE workspace_id=$1 AND installation_id=$2`,testWorkspaceID,installationID) })
			codeCalls := 0
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
				switch r.URL.Path {
				case "/login/oauth/access_token":
					codeCalls++
					if r.Method != http.MethodPost || r.ParseForm() != nil || r.Form.Get("client_secret") != "test-client-secret" || r.Form.Get("code") != "one-use-test-code" || !strings.HasSuffix(r.Form.Get("redirect_uri"),"/api/github/authorize") { t.Error("invalid code exchange") }
					if tc.badExchange || codeCalls > 1 { writeJSON(w,http.StatusBadRequest,map[string]string{"error":"bad_verification_code"}); return }
					writeJSON(w,http.StatusOK,map[string]string{"access_token":"ephemeral-test-token","token_type":"bearer"})
				case "/user/installations":
					if r.Header.Get("Authorization") != "Bearer ephemeral-test-token" { t.Error("installation verification did not use the user token") }
					if tc.denied { writeJSON(w,http.StatusOK,map[string]any{"installations":[]any{}}); return }
					var suspended any
					if tc.suspended { suspended = "2026-09-14T00:00:00Z" }
					writeJSON(w,http.StatusOK,map[string]any{"installations":[]any{map[string]any{"id":installationID,"suspended_at":suspended,"account":map[string]string{"login":"authorized-org","type":"Organization"}}}})
				default: t.Errorf("unexpected provider request: %s",r.URL.Path); w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer provider.Close()
			oldAPI,oldOAuth := githubAPIBase,githubOAuthBase
			githubAPIBase,githubOAuthBase = provider.URL,provider.URL
			t.Cleanup(func(){ githubAPIBase,githubOAuthBase = oldAPI,oldOAuth })
			req := httptest.NewRequest(http.MethodGet,fmt.Sprintf("/api/github/setup?installation_id=%d&setup_action=update",installationID),nil)
			if tc.withState { req.URL.RawQuery += "&state="+url.QueryEscape(value) }
			req.AddCookie(&http.Cookie{Name:githubConnectCookie,Value:value})
			rec := httptest.NewRecorder(); testHandler.GitHubSetupCallback(rec,req)
			assertBinding := func(want int){
				t.Helper(); var count int
				if err := testPool.QueryRow(t.Context(),`SELECT count(*) FROM github_installation WHERE workspace_id=$1 AND installation_id=$2`,testWorkspaceID,installationID).Scan(&count); err != nil || count != want { t.Fatalf("binding count=%d, want %d: %v",count,want,err) }
			}
			assertBinding(0)
			if tc.noConfig { if !strings.Contains(rec.Header().Get("Location"),"github_error=user_authorization_not_configured") { t.Fatal("missing OAuth configuration not reported") }; return }
			authorizeURL,err := url.Parse(rec.Header().Get("Location")); if err != nil { t.Fatal(err) }
			if authorizeURL.Path != "/login/oauth/authorize" || authorizeURL.Query().Get("client_id") != "test-client" { t.Fatal("reconnect skipped GitHub user authorization") }
			if tc.revoked {
				if _,err := testPool.Exec(t.Context(),`UPDATE member SET role='member' WHERE workspace_id=$1 AND user_id=$2`,testWorkspaceID,testUserID); err != nil { t.Fatal(err) }
				t.Cleanup(func(){ _,_ = testPool.Exec(context.Background(),`UPDATE member SET role='owner' WHERE workspace_id=$1 AND user_id=$2`,testWorkspaceID,testUserID) })
			}
			callback := httptest.NewRequest(http.MethodGet,"/api/github/authorize?code=one-use-test-code&state="+url.QueryEscape(authorizeURL.Query().Get("state")),nil)
			callback.AddCookie(rec.Result().Cookies()[0])
			rec = httptest.NewRecorder(); testHandler.GitHubAuthorizeCallback(rec,callback)
			if tc.denied || tc.suspended || tc.revoked || tc.badExchange {
				assertBinding(0)
				if !strings.Contains(rec.Header().Get("Location"),"github_error=") { t.Fatal("authorization failure not reported") }
				if tc.revoked && codeCalls != 0 { t.Fatal("revoked workspace member reached GitHub code exchange") }
				return
			}
			assertBinding(1)
			if !strings.Contains(rec.Header().Get("Location"),"/handler-tests/settings?tab=repositories&github_connected=1") { t.Fatal("success returned to the wrong workspace") }
			if rec.Result().Cookies()[0].MaxAge != -1 { t.Fatal("completed browser context not cleared") }
			var userID,login string
			if err := testPool.QueryRow(t.Context(),`SELECT connected_by_id,account_login FROM github_installation WHERE workspace_id=$1 AND installation_id=$2`,testWorkspaceID,installationID).Scan(&userID,&login); err != nil || userID != testUserID || login != "authorized-org" { t.Fatalf("wrong connecting actor/account: %v",err) }
			// Reusing the consumed OAuth code cannot write another binding.
			rec = httptest.NewRecorder(); testHandler.GitHubAuthorizeCallback(rec,callback)
			if !strings.Contains(rec.Header().Get("Location"),"github_error=installation_not_authorized") { t.Fatal("consumed OAuth code accepted") }
			assertBinding(1)
		})
	}
}

func TestGitHubUserInstallationVerificationPaginates(t *testing.T) {
	t.Setenv("GITHUB_APP_CLIENT_ID","test-client"); t.Setenv("GITHUB_APP_CLIENT_SECRET","test-secret")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
		if r.URL.Path == "/login/oauth/access_token" { writeJSON(w,http.StatusOK,map[string]string{"access_token":"ephemeral-test-token","token_type":"bearer"}); return }
		if r.URL.Query().Get("page") == "1" { writeJSON(w,http.StatusOK,map[string]any{"installations":make([]githubAuthorizedInstallation,100)}); return }
		_,_ = w.Write([]byte(`{"installations":[{"id":123,"account":{"login":"second-page","type":"User"}}]}`))
	}))
	defer provider.Close()
	oldAPI,oldOAuth := githubAPIBase,githubOAuthBase; githubAPIBase,githubOAuthBase = provider.URL,provider.URL
	t.Cleanup(func(){ githubAPIBase,githubOAuthBase = oldAPI,oldOAuth })
	installation,err := testHandler.authorizedGitHubInstallation(t.Context(),"test-code",123)
	if err != nil || installation.ID != 123 { t.Fatalf("missing installation on later page: %v",err) }
}
