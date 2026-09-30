package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
			if !strings.HasPrefix(started.AuthorizeURL, f.provider.URL+"/") || query.Get("redirect_uri") != wantOrigin+wantPath || !isConnectorOAuthState(state) {
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
