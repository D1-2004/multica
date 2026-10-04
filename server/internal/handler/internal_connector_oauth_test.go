package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/connectorcatalog"
	"github.com/multica-ai/multica/server/internal/contextcap"
)

// Connecting the workspace shared account always binds the browser that
// receives the start response: the start answers with the provider's
// authorize URL and a binding cookie on the callback path, never with a link
// that binds whichever browser opens it. An admin who hands the provider URL
// to someone else cannot get that person's account into the workspace: their
// browser has no cookie, so the callback is refused and nothing is stored.
func TestCatalogConnectorWorkspaceStartBindsTheStartingBrowser(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	for _, app := range []connectorcatalog.App{f.dcr, f.gh} {
		c := f.create(t, app)
		scope := f.scope(c.ID, connectorOAuthScopeWorkspace, "")
		wantOrigin, wantPath, via := catalogAppOrigin, connectorOAuthCallbackPath, connectorOAuthViaDCR
		if app.AuthKind == connectorcatalog.AuthOAuthGitHubApp {
			wantOrigin, wantPath, via = catalogWebOrigin, connectorOAuthGitHubCallback, connectorOAuthViaGitHub
		}
		start := func() (string, *http.Cookie) {
			t.Helper()
			started, err := f.h.startConnectorOAuth(ctx, connectorOAuthStart{connectorOAuthScope: scope})
			if err != nil {
				t.Fatal(err)
			}
			query := f.provideAuthorizeURL(t, started.AuthorizeURL)
			state := query.Get("state")
			if app.AuthKind == connectorcatalog.AuthOAuthGitHubApp {
				parsed, parseErr := url.Parse(started.AuthorizeURL)
				if parseErr != nil || parsed.Host != "github.com" || parsed.Path != "/apps/"+os.Getenv("GITHUB_APP_SLUG")+"/installations/new" ||
					query.Get("client_id") != "" || query.Get("state") == "" || !isConnectorOAuthState(state) {
					t.Fatalf("%s start = %s", app.Slug, started.AuthorizeURL)
				}
			} else if !strings.HasPrefix(started.AuthorizeURL, f.provider.URL+"/") || query.Get("redirect_uri") != wantOrigin+wantPath || !isConnectorOAuthState(state) {
				t.Fatalf("%s start = %s", app.Slug, started.AuthorizeURL)
			}
			cookie := started.Cookie
			if cookie == nil || cookie.Name != connectorOAuthCookieName(hashConnectorOAuthState(state)) || cookie.Path != wantPath || !cookie.HttpOnly ||
				!cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Value == "" {
				t.Fatalf("%s binding cookie = %+v", app.Slug, cookie)
			}
			return state, cookie
		}

		// Forwarded: the provider URL completes in a browser without the cookie.
		forwarded, _ := start()
		if outcome := f.h.completeConnectorOAuth(ctx, connectorOAuthCallback{Via: via, State: forwarded, Code: "good-code"}); outcome.ErrorCode != connectOAuthErrBrowserMismatch {
			t.Fatalf("%s callback from another browser = %+v", app.Slug, outcome)
		}
		var ciphertext []byte
		if err := testPool.QueryRow(ctx, `SELECT credential_ciphertext FROM internal_connector WHERE id = $1`, c.ID).Scan(&ciphertext); err != nil || ciphertext != nil {
			t.Fatalf("%s workspace credential after a forwarded connect = %v %v", app.Slug, ciphertext, err)
		}

		state, cookie := start()
		outcome := f.h.completeConnectorOAuth(ctx, connectorOAuthCallback{Via: via, State: state, Code: "good-code", BrowserNonce: cookie.Value})
		if outcome.ErrorCode != "" || outcome.Account != "octo" {
			t.Fatalf("%s connect = %+v", app.Slug, outcome)
		}
	}
}

// A refresh the provider already answered is stored even when the relay
// request that triggered it is cancelled: rotating providers invalidate the
// old refresh token as soon as they answer.
func TestCatalogConnectorRefreshSurvivesCallerCancellation(t *testing.T) {
	f := newCatalogFixture(t)
	c := f.create(t, f.gh)
	key := f.personKey(c.ID)
	f.sealPersonOAuth(t, c.ID, contextcap.OAuthToken{AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(10 * time.Second).Unix(), Account: "octo"})
	f.provider.mu.Lock()
	f.provider.refreshDelay = 300 * time.Millisecond
	f.provider.mu.Unlock()
	credential, err := contextcap.GetCredential(context.Background(), testPool, key)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := contextcap.OpenCredentialSecret(f.box, key, credential.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	resolved := c
	resolved.setResolvedSecret(secret, contextcap.ScopePerson, key)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	token, err := f.h.freshConnectorToken(ctx, &resolved, "")
	credential, getErr := contextcap.GetCredential(context.Background(), testPool, key)
	if getErr != nil {
		t.Fatal(getErr)
	}
	stored, openErr := contextcap.OpenCredentialSecret(f.box, key, credential.Ciphertext)
	if openErr != nil || stored.OAuth == nil || stored.OAuth.RefreshToken == "old-refresh" || stored.Bearer == "old-access" {
		t.Fatalf("stored after a cancelled caller = %+v %v", stored, openErr)
	}
	if err != nil || token != stored.Bearer {
		t.Fatalf("fresh token = %q %v, stored %q", token, err, stored.Bearer)
	}
	if stored.OAuth.ClientID != "gh-client" {
		t.Fatalf("refreshed token client = %q", stored.OAuth.ClientID)
	}
}

// A refresh never queues on the credential's row lock while another replica
// asks the provider: a caller whose token still works keeps using it, one
// that needs the new token polls for it outside any transaction. A failed
// refresh is not retried by every following call.
func TestCatalogConnectorRefreshDoesNotQueueOnTheRowLock(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	c := f.create(t, f.gh)
	key := f.personKey(c.ID)
	resolve := func() internalConnector {
		t.Helper()
		credential, err := contextcap.GetCredential(ctx, testPool, key)
		if err != nil {
			t.Fatal(err)
		}
		secret, err := contextcap.OpenCredentialSecret(f.box, key, credential.Ciphertext)
		if err != nil {
			t.Fatal(err)
		}
		resolved := c
		resolved.setResolvedSecret(secret, contextcap.ScopePerson, key)
		return resolved
	}
	refreshes := func() int {
		_, _, n, _ := f.provider.counts()
		return n
	}
	otherReplica := func() pgx.Tx {
		t.Helper()
		tx, err := testPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := contextcap.LockCredential(ctx, tx, key); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		return tx
	}

	// The token still works: the caller does not wait for the other
	// replica's refresh.
	f.sealPersonOAuth(t, c.ID, contextcap.OAuthToken{AccessToken: "soon-access", RefreshToken: "soon-refresh", ExpiresAt: time.Now().Add(10 * time.Second).Unix(), Account: "octo"})
	resolved := resolve()
	t.Cleanup(func() { connectorRefreshes.clear(connectorRefreshKey(&resolved)) })
	other := otherReplica()
	started := time.Now()
	token, err := f.h.freshConnectorToken(ctx, &resolved, "")
	_ = other.Rollback(ctx)
	if err != nil || token != "soon-access" || time.Since(started) > 2*time.Second || refreshes() != 0 {
		t.Fatalf("locked refresh with a working token = %q %v after %s, refreshes %d", token, err, time.Since(started), refreshes())
	}

	// The token expired: the caller picks up the other replica's result
	// once it commits, without a token request of its own.
	f.sealPersonOAuth(t, c.ID, contextcap.OAuthToken{AccessToken: "expired-access", RefreshToken: "expired-refresh", ExpiresAt: time.Now().Add(-time.Minute).Unix(), Account: "octo"})
	resolved = resolve()
	other = otherReplica()
	sealed, err := contextcap.SealOAuthCredential(f.box, key, contextcap.OAuthToken{AccessToken: "other-replica-access", RefreshToken: "other-replica-refresh",
		ExpiresAt: time.Now().Add(time.Hour).Unix(), Account: "octo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contextcap.ReplaceCredentialSecret(ctx, other, key, sealed, contextcap.OAuthHint("octo")); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(400 * time.Millisecond)
		_ = other.Commit(context.Background())
	}()
	token, err = f.h.freshConnectorToken(ctx, &resolved, "")
	if err != nil || token != "other-replica-access" || refreshes() != 0 {
		t.Fatalf("expired token behind another replica's refresh = %q %v, refreshes %d", token, err, refreshes())
	}

	// The provider fails: the working token is used and the next calls do
	// not ask again until the backoff passes.
	f.provider.mu.Lock()
	f.provider.refreshMode = "error"
	f.provider.mu.Unlock()
	f.sealPersonOAuth(t, c.ID, contextcap.OAuthToken{AccessToken: "soon-access-2", RefreshToken: "soon-refresh-2", ExpiresAt: time.Now().Add(10 * time.Second).Unix(), Account: "octo"})
	for i := 0; i < 3; i++ {
		resolved = resolve()
		if token, err := f.h.freshConnectorToken(ctx, &resolved, ""); err != nil || token != "soon-access-2" {
			t.Fatalf("call %d with a failing provider = %q %v", i, token, err)
		}
	}
	if refreshes() != 1 {
		t.Fatalf("failing provider asked %d times, want 1", refreshes())
	}
	f.sealPersonOAuth(t, c.ID, contextcap.OAuthToken{AccessToken: "expired-access-2", RefreshToken: "expired-refresh-2", ExpiresAt: time.Now().Add(-time.Minute).Unix(), Account: "octo"})
	resolved = resolve()
	if _, err := f.h.freshConnectorToken(ctx, &resolved, ""); !errors.Is(err, errConnectorRefreshBackoff) || refreshes() != 1 {
		t.Fatalf("expired token during the backoff = %v, refreshes %d", err, refreshes())
	}
}

// When the app origin changes, the connector registers a new client for the
// new redirect URI but keeps the old one: tokens it issued are refreshed
// with it instead of failing with invalid_grant and being deleted. Changing
// back promotes the kept registration without registering again.
func TestCatalogConnectorRegistrationChangeKeepsTheIssuingClient(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	c := f.storeTools(t, f.create(t, f.dcr))
	f.offer(t, c.ID)
	f.grant(t, contextcap.ScopePerson, catalogTestStaff)
	scope := f.scope(c.ID, contextcap.ScopePerson, catalogTestStaff)
	_, query, nonce := f.start(t, scope, "")
	if query.Get("client_id") != "dcr-client-1" {
		t.Fatalf("first client = %q", query.Get("client_id"))
	}
	// A connect started before the change completes after it with the
	// client it was started with.
	f.h.cfg.AppURL = "https://app2.example.test"
	_, second, _ := f.start(t, scope, "")
	if second.Get("client_id") != "dcr-client-2" || second.Get("redirect_uri") != "https://app2.example.test"+connectorOAuthCallbackPath {
		t.Fatalf("client after the origin change = %v", second)
	}
	// The provider completes the first authorization (its challenge and
	// redirect URI).
	f.provideAuthorizeURL(t, "https://provider.example.test/authorize?"+query.Encode())
	outcome := f.h.completeConnectorOAuth(ctx, connectorOAuthCallback{Via: connectorOAuthViaDCR, State: query.Get("state"), Code: "good-code", BrowserNonce: nonce})
	if outcome.ErrorCode != "" {
		t.Fatalf("connect started before the origin change = %+v", outcome)
	}
	key := f.personKey(c.ID)
	credential, err := contextcap.GetCredential(ctx, testPool, key)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := contextcap.OpenCredentialSecret(f.box, key, credential.Ciphertext)
	if err != nil || secret.OAuth == nil || secret.OAuth.ClientID != "dcr-client-1" {
		t.Fatalf("stored token = %+v %v", secret, err)
	}

	// The token nears expiry: it is refreshed with the client that issued it.
	f.sealPersonOAuth(t, c.ID, contextcap.OAuthToken{AccessToken: "acc-issued-by-1", RefreshToken: "ref-issued-by-1",
		ExpiresAt: time.Now().Add(10 * time.Second).Unix(), ClientID: "dcr-client-1"})
	resolve := func() internalConnector {
		t.Helper()
		credential, err := contextcap.GetCredential(ctx, testPool, key)
		if err != nil {
			t.Fatal(err)
		}
		secret, err := contextcap.OpenCredentialSecret(f.box, key, credential.Ciphertext)
		if err != nil {
			t.Fatal(err)
		}
		resolved := c
		resolved.setResolvedSecret(secret, contextcap.ScopePerson, key)
		return resolved
	}
	resolved := resolve()
	if _, err := f.h.freshConnectorToken(ctx, &resolved, ""); err != nil {
		t.Fatal(err)
	}
	f.provider.mu.Lock()
	refreshClient := f.provider.lastRefreshClient
	f.provider.mu.Unlock()
	if refreshClient != "dcr-client-1" {
		t.Fatalf("refreshed with client %q, want the issuing dcr-client-1", refreshClient)
	}

	// A token whose client is no longer known cannot be refreshed, but it is
	// not treated as revoked either.
	f.sealPersonOAuth(t, c.ID, contextcap.OAuthToken{AccessToken: "acc-unknown", RefreshToken: "ref-unknown",
		ExpiresAt: time.Now().Add(-time.Minute).Unix(), ClientID: "dcr-client-gone"})
	_, _, refreshesBefore, _ := f.provider.counts()
	resolved = resolve()
	if _, err := f.h.freshConnectorToken(ctx, &resolved, ""); !errors.Is(err, errConnectorReconnectRequired) {
		t.Fatalf("unknown client refresh: %v", err)
	}
	if _, err := contextcap.GetCredential(ctx, testPool, key); err != nil {
		t.Fatalf("credential of an unknown client was deleted: %v", err)
	}
	if _, _, refreshes, _ := f.provider.counts(); refreshes != refreshesBefore {
		t.Fatalf("token endpoint called for an unknown client: %d -> %d", refreshesBefore, refreshes)
	}

	// The origin changes back: the kept registration is promoted.
	f.h.cfg.AppURL = catalogAppOrigin
	_, third, _ := f.start(t, scope, "")
	if registrations, _, _, _ := f.provider.counts(); registrations != 2 || third.Get("client_id") != "dcr-client-1" {
		t.Fatalf("after changing back: registrations=%d client=%q", registrations, third.Get("client_id"))
	}
	record, err := f.h.loadConnectorOAuthClient(ctx, testWorkspaceID, c.ID)
	if err != nil || len(record.Previous) != 1 || record.Previous[0].Registration.ClientID != "dcr-client-2" {
		t.Fatalf("registration history = %+v %v", record.Previous, err)
	}
}

// Refreshing tools skips a credential that cannot list them (a revoked
// PAT, a failed refresh) instead of failing with a 500, and reports
// discovery_failed when no credential works.
func TestCatalogConnectorRefreshToolsSkipsCredentialsThatCannotList(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	c := f.create(t, f.gh)
	sealed, err := f.h.sealWorkspaceConnectorSecret(testWorkspaceID, c.ID, contextcap.Secret{Bearer: "revoked-pat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE internal_connector SET credential_ciphertext = $2 WHERE id = $1`, c.ID, sealed); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.refreshCatalogConnectorTools(ctx, testWorkspaceID, c.ID); !errors.Is(err, errCatalogDiscoveryFailed) {
		t.Fatalf("refresh with only a revoked PAT: %v", err)
	}
	rec := httptest.NewRecorder()
	f.h.RefreshInternalConnectorTools(rec, withURLParams(newRequest(http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/internal-connectors/"+c.ID+"/tools/refresh", nil),
		"id", testWorkspaceID, "connectorId", c.ID))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "discovery_failed") {
		t.Fatalf("refresh tools route: %d %s", rec.Code, rec.Body.String())
	}

	// A person's working account is used after the revoked workspace PAT.
	f.addValidToken("person-access")
	f.sealPersonOAuth(t, c.ID, contextcap.OAuthToken{AccessToken: "person-access", Account: "octo"})
	result, err := f.h.refreshCatalogConnectorTools(ctx, testWorkspaceID, c.ID)
	if err != nil || result.Discovered != 2 || strings.Join(result.AllowedTools, ",") != "search" {
		t.Fatalf("refresh with a person's account after a revoked PAT = %+v %v", result, err)
	}
}

// The router rate-limits GitHub App callbacks that complete a connector
// connect, and only those.
func TestPreregisteredAuthorizeURLPinsAsanaResource(t *testing.T) {
	got, err := preregisteredAuthorizeURL(
		"https://app.asana.com/-/oauth_authorize", "cid", "",
		"https://example.test/cb", "state", "verifier", "https://mcp.asana.com/v2",
	)
	if err != nil || !strings.Contains(got, "resource=https%3A%2F%2Fmcp.asana.com%2Fv2") ||
		strings.Contains(got, "scope=") || !strings.Contains(got, "response_type=code") {
		t.Fatalf("asana authorize = %s %v", got, err)
	}
	slack, err := preregisteredAuthorizeURL(
		"https://slack.com/oauth/v2_user/authorize", "cid", "search:read.public",
		"https://example.test/cb", "state", "verifier", "",
	)
	if err != nil || strings.Contains(slack, "resource=") || strings.Contains(slack, "prompt=") || !strings.Contains(slack, "scope=search") {
		t.Fatalf("slack authorize = %s %v", slack, err)
	}
	microsoft, err := preregisteredAuthorizeURL(
		"https://login.microsoftonline.com/common/oauth2/v2.0/authorize", "cid",
		"offline_access openid profile email User.Read Mail.Read Calendars.Read Contacts.Read",
		"https://fde-workbench.dingtalk.com/api/connectors/oauth/callback", "state", "verifier", "",
	)
	if err != nil || !strings.Contains(microsoft, "prompt=select_account") || !strings.Contains(microsoft, "response_type=code") ||
		!strings.Contains(microsoft, "code_challenge_method=S256") || strings.Contains(microsoft, "resource=") {
		t.Fatalf("microsoft authorize = %s %v", microsoft, err)
	}
	if !strings.Contains(got, "response_type=code") || strings.Contains(got, "prompt=") {
		t.Fatalf("asana authorize gained a microsoft parameter: %s", got)
	}
}

func TestConnectorOAuthShareableStaysOnSceneAndPerson(t *testing.T) {
	scene := connectorOAuthScope{ScopeType: contextcap.ScopeScene}
	person := connectorOAuthScope{ScopeType: contextcap.ScopePerson}
	workspace := connectorOAuthScope{ScopeType: connectorOAuthScopeWorkspace}
	github := connectorSealedVerifier{Shareable: true, Via: connectorOAuthViaGitHub, AuthFlow: connectorOAuthFlowInstall}
	outlook := connectorSealedVerifier{Shareable: true, Via: connectorOAuthViaDCR, AuthFlow: connectorOAuthFlowOutlook}
	notion := connectorSealedVerifier{Shareable: true, Via: connectorOAuthViaDCR}
	if !connectorOAuthShareable(github, scene) || !connectorOAuthShareable(github, person) || connectorOAuthShareable(github, workspace) {
		t.Fatal("github shareable scope")
	}
	if !connectorOAuthShareable(outlook, scene) || !connectorOAuthShareable(outlook, person) || connectorOAuthShareable(outlook, workspace) {
		t.Fatal("outlook shareable scope")
	}
	if connectorOAuthShareable(notion, scene) || connectorOAuthShareable(connectorSealedVerifier{Via: connectorOAuthViaDCR, AuthFlow: connectorOAuthFlowOutlook}, scene) {
		t.Fatal("dcr connect must stay bound to the starting browser")
	}
}

func TestGitHubAppInstallAuthorizeURLRejectsABadSlug(t *testing.T) {
	got, err := githubAppInstallAuthorizeURL("qwen-tag-pre", "mcpc.abc")
	if err != nil || got != "https://github.com/apps/qwen-tag-pre/installations/new?state=mcpc.abc" {
		t.Fatalf("install URL = %s %v", got, err)
	}
	for _, slug := range []string{"", "a/b", "a?b", "a#b"} {
		if _, err := githubAppInstallAuthorizeURL(slug, "mcpc.abc"); err == nil {
			t.Fatalf("slug %q was accepted", slug)
		}
	}
	if parseConnectorOAuthInstallationID("12") != 12 || parseConnectorOAuthInstallationID("0") != 0 ||
		parseConnectorOAuthInstallationID("x") != 0 || parseConnectorOAuthInstallationID("-3") != 0 {
		t.Fatal("installation id")
	}
	if parseConnectorOAuthSetupAction(" install ") != "install" || parseConnectorOAuthSetupAction("update") != "update" ||
		parseConnectorOAuthSetupAction("delete") != "" {
		t.Fatal("setup action")
	}
}

func TestIsConnectorOAuthCallbackSelectsConnectorStates(t *testing.T) {
	for target, want := range map[string]bool{
		"/api/github/authorize?code=c&state=mcpc.abc":             true,
		"/api/github/authorize?code=c&state=eyJhbGciOiJIUzI1NiJ9": false,
		"/api/github/authorize?code=c":                            false,
	} {
		if got := IsConnectorOAuthCallback(httptest.NewRequest(http.MethodGet, target, nil)); got != want {
			t.Fatalf("IsConnectorOAuthCallback(%s) = %v, want %v", target, got, want)
		}
	}
}
