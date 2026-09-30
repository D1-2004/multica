package handler

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/contextcap"
)

const (
	forwardProdOrigin = "https://prod.example.test"
	forwardRandom     = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
)

func forwardState(home string) string {
	state := connectorOAuthStatePrefix + forwardRandom
	if home != "" {
		state += "." + base64.RawURLEncoding.EncodeToString([]byte(home))
	}
	return state
}

func TestConnectorOAuthStateHomeOrigin(t *testing.T) {
	if len(forwardRandom) != connectorOAuthStateRandomLength {
		t.Fatal("fixture random part has the wrong length")
	}
	for state, want := range map[string]string{
		forwardState(""):                              "",
		forwardState("https://pre.example.test"):      "https://pre.example.test",
		forwardState("http://localhost:3000"):         "http://localhost:3000",
		forwardState("https://pre.example.test:8443"): "https://pre.example.test:8443",
	} {
		home, ok := splitConnectorOAuthState(state)
		if !ok || home != want || !validConnectorOAuthState(state) {
			t.Errorf("splitConnectorOAuthState(%q) = %q %v, want %q", state, home, ok, want)
		}
	}
	for _, bad := range []string{
		"", "mcpc.", forwardRandom, "mcpc.short", connectorOAuthStatePrefix + forwardRandom + ".",
		connectorOAuthStatePrefix + forwardRandom + "x", connectorOAuthStatePrefix + forwardRandom + ".!!",
		forwardState("https://PRE.example.test"), forwardState("https://pre.example.test/"), forwardState("https://pre.example.test/path"),
		forwardState("ftp://pre.example.test"), forwardState("https://user@pre.example.test"), forwardState("https://pre.example.test?x=1"),
		forwardState("pre.example.test"), forwardState("https://" + strings.Repeat("a", connectorOAuthMaxHomeOrigin)),
	} {
		if _, ok := splitConnectorOAuthState(bad); ok || validConnectorOAuthState(bad) {
			t.Errorf("splitConnectorOAuthState(%q) accepted", bad)
		}
	}
	for raw, want := range map[string]string{
		"https://Prod.Example.test/": "https://prod.example.test",
		" http://localhost:3000 ":    "http://localhost:3000",
	} {
		if got, ok := normalizeConnectorOAuthOrigin(raw); !ok || got != want {
			t.Errorf("normalizeConnectorOAuthOrigin(%q) = %q %v", raw, got, ok)
		}
	}
	for _, bad := range []string{"", "prod.example.test", "https://", "https://prod.example.test/api", "javascript:alert(1)", "https://a b"} {
		if got, ok := normalizeConnectorOAuthOrigin(bad); ok {
			t.Errorf("normalizeConnectorOAuthOrigin(%q) = %q", bad, got)
		}
	}
	t.Setenv(connectorOAuthForwardOriginsEnv, " https://pre.example.test/ ,http://insecure.example.test,,not an origin, https://Other.example.test")
	if got := strings.Join(connectorOAuthForwardOrigins(), ","); got != "https://pre.example.test,https://other.example.test" {
		t.Fatalf("forward origins = %q", got)
	}
	t.Setenv(connectorOAuthClientNameEnv, "")
	if connectorOAuthClientName() != "Multica" {
		t.Fatal("default client name")
	}
	t.Setenv(connectorOAuthClientNameEnv, "  QwenTagPre ")
	if connectorOAuthClientName() != "QwenTagPre" {
		t.Fatal("configured client name")
	}
	t.Setenv(connectorOAuthClientNameEnv, "bad\nname")
	if connectorOAuthClientName() != "Multica" {
		t.Fatal("client name with a control character")
	}
}

// The callback deployment forwards a state that names another listed https
// origin to that origin's same callback path and raw query, before any local
// handling, on both callback routes; anything else is refused.
func TestConnectorOAuthCallbackForwarding(t *testing.T) {
	prod := &Handler{cfg: Config{AppURL: forwardProdOrigin, FrontendOrigin: forwardProdOrigin}}
	router := catalogAPIRouter(prod)
	t.Setenv(connectorOAuthForwardOriginsEnv, "https://pre.example.test,http://insecure.example.test")

	for _, path := range []string{ConnectorOAuthCallbackPath, connectorOAuthGitHubCallback} {
		state := forwardState("https://pre.example.test")
		rawQuery := "code=the+code&state=" + url.QueryEscape(state) + "&iss=https%3A%2F%2Fprovider.example"
		rec := browserGet(router, path+"?"+rawQuery)
		want := "https://pre.example.test" + path + "?" + rawQuery
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != want || rec.Header().Get("Referrer-Policy") != "no-referrer" ||
			rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s forward: %d %q, want %q", path, rec.Code, rec.Header().Get("Location"), want)
		}
		for name, home := range map[string]string{
			"unlisted origin":       "https://evil.example.test",
			"listed http origin":    "http://insecure.example.test",
			"listed host, http":     "http://pre.example.test",
			"listed host, a port":   "https://pre.example.test:8443",
			"a subdomain of listed": "https://x.pre.example.test",
		} {
			rec := browserGet(router, path+"?code=c&state="+url.QueryEscape(forwardState(home)))
			if rec.Code != http.StatusBadRequest || rec.Header().Get("Location") != "" || !strings.Contains(rec.Body.String(), "连接已失效") {
				t.Fatalf("%s %s: %d %q", path, name, rec.Code, rec.Header().Get("Location"))
			}
		}
	}
	// A state naming this deployment itself is handled locally (here: an
	// unknown state, so the local invalid-connection page, not a redirect).
	own := httptest.NewRecorder()
	if prod.forwardConnectorOAuthCallback(own, httptest.NewRequest(http.MethodGet, ConnectorOAuthCallbackPath+"?state="+url.QueryEscape(forwardState(forwardProdOrigin)), nil), connectorOAuthViaDCR) {
		t.Fatal("a state of this deployment was forwarded")
	}
	// States without a home origin and malformed states stay local too.
	for _, state := range []string{forwardState(""), "mcpc.unknown", "not-a-connector-state"} {
		if prod.forwardConnectorOAuthCallback(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, ConnectorOAuthCallbackPath+"?state="+url.QueryEscape(state), nil), connectorOAuthViaDCR) {
			t.Fatalf("state %q was forwarded", state)
		}
	}
	// Without a forward list every foreign state is refused.
	t.Setenv(connectorOAuthForwardOriginsEnv, "")
	rec := browserGet(router, ConnectorOAuthCallbackPath+"?code=c&state="+url.QueryEscape(forwardState("https://pre.example.test")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("forward without a list: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// The router's GitHub dispatch still sends such states to the connector
	// flow.
	if !IsConnectorOAuthCallback(httptest.NewRequest(http.MethodGet, connectorOAuthGitHubCallback+"?state="+url.QueryEscape(forwardState("https://pre.example.test")), nil)) {
		t.Fatal("a state with a home origin is not a connector callback")
	}
}

// Pre-release sends providers the production callback: the redirect URI
// moves to the callback origin, the state names this deployment, the binding
// cookie stays on this deployment's own origin, and the connect completes
// here once production forwards the callback.
func TestConnectorOAuthCallbackOriginRoundTrip(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	homeRouter := catalogAPIRouter(f.h)
	prod := &Handler{cfg: Config{AppURL: forwardProdOrigin, FrontendOrigin: forwardProdOrigin}}
	prodRouter := catalogAPIRouter(prod)
	dcr := f.storeTools(t, f.create(t, f.dcr))
	gh := f.storeTools(t, f.create(t, f.gh))
	f.offer(t, dcr.ID, gh.ID)
	f.grant(t, contextcap.ScopePerson, catalogTestStaff)
	scope := func(connectorID string) connectorOAuthScope {
		return f.scope(connectorID, contextcap.ScopePerson, catalogTestStaff)
	}

	// Without an override the redirect URI and state are unchanged.
	_, query, _ := f.start(t, scope(dcr.ID), "")
	if query.Get("redirect_uri") != catalogAppOrigin+connectorOAuthCallbackPath || strings.Contains(strings.TrimPrefix(query.Get("state"), connectorOAuthStatePrefix), ".") {
		t.Fatalf("start without an override = %v", query)
	}
	if registrations, _, _, _ := f.provider.counts(); registrations != 1 {
		t.Fatalf("registrations = %d", registrations)
	}

	t.Setenv(connectorOAuthCallbackOriginEnv, forwardProdOrigin+"/")
	t.Setenv(connectorOAuthForwardOriginsEnv, catalogAppOrigin+","+catalogWebOrigin)
	for _, tc := range []struct {
		connector internalConnector
		path      string
		home      string
		via       string
	}{
		{dcr, connectorOAuthCallbackPath, catalogAppOrigin, connectorOAuthViaDCR},
		{gh, connectorOAuthGitHubCallback, catalogWebOrigin, connectorOAuthViaGitHub},
	} {
		started, err := f.h.startConnectorOAuth(ctx, connectorOAuthStart{connectorOAuthScope: scope(tc.connector.ID)})
		if err != nil {
			t.Fatal(err)
		}
		query := f.provideAuthorizeURL(t, started.AuthorizeURL)
		state := query.Get("state")
		if home, ok := splitConnectorOAuthState(state); !ok || home != tc.home {
			t.Fatalf("%s state %q home = %q %v", tc.via, state, home, ok)
		}
		if query.Get("redirect_uri") != forwardProdOrigin+tc.path {
			t.Fatalf("%s redirect_uri = %q", tc.via, query.Get("redirect_uri"))
		}
		cookie := started.Cookie
		if cookie.Path != tc.path || !cookie.Secure || cookie.Name != connectorOAuthCookieName(hashConnectorOAuthState(state)) {
			t.Fatalf("%s cookie = %+v", tc.via, cookie)
		}
		// The provider returns to production, which forwards the callback
		// home; the browser there still has the binding cookie.
		rawQuery := "code=good-code&state=" + url.QueryEscape(state)
		forwarded := browserGet(prodRouter, tc.path+"?"+rawQuery)
		if forwarded.Code != http.StatusFound || forwarded.Header().Get("Location") != tc.home+tc.path+"?"+rawQuery {
			t.Fatalf("%s forward: %d %q", tc.via, forwarded.Code, forwarded.Header().Get("Location"))
		}
		location, err := url.Parse(forwarded.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		// The override is removed while the connect is in flight (a restart
		// or rolling deploy): the code is still exchanged with the redirect
		// URI the authorization was requested with (the provider rejects
		// any other).
		t.Setenv(connectorOAuthCallbackOriginEnv, "")
		done := browserGet(homeRouter, location.RequestURI(), cookie)
		if done.Code != http.StatusFound || !strings.Contains(done.Header().Get("Location"), "connected="+url.QueryEscape(tc.connector.CatalogSlug)) {
			t.Fatalf("%s completion at home: %d %q", tc.via, done.Code, done.Header().Get("Location"))
		}
		t.Setenv(connectorOAuthCallbackOriginEnv, forwardProdOrigin+"/")
	}
	if _, err := contextcap.GetCredential(ctx, testPool, f.personKey(dcr.ID)); err != nil {
		t.Fatalf("forwarded connect stored nothing: %v", err)
	}
	// The DCR redirect URI changed, so a new client was registered for it.
	if registrations, _, _, _ := f.provider.counts(); registrations != 2 {
		t.Fatalf("registrations after the callback origin change = %d", registrations)
	}
	f.provider.mu.Lock()
	registeredRedirect := f.provider.lastRegisteredRedirect
	f.provider.mu.Unlock()
	if registeredRedirect != forwardProdOrigin+connectorOAuthCallbackPath {
		t.Fatalf("registered redirect = %q", registeredRedirect)
	}

	// The cookie follows this deployment's own origin, never the callback
	// origin: an http own origin gets no Secure cookie even though the
	// callback origin is https.
	plain := *f.h
	plain.cfg.AppURL, plain.cfg.FrontendOrigin = "http://pre.local.test", "http://pre.local.test"
	started, err := plain.startConnectorOAuth(ctx, connectorOAuthStart{connectorOAuthScope: scope(dcr.ID)})
	if err != nil {
		t.Fatal(err)
	}
	query = f.provideAuthorizeURL(t, started.AuthorizeURL)
	if home, _ := splitConnectorOAuthState(query.Get("state")); home != "http://pre.local.test" || started.Cookie.Secure {
		t.Fatalf("http own origin: home=%q cookie=%+v", home, started.Cookie)
	}

	// A malformed override is ignored.
	t.Setenv(connectorOAuthCallbackOriginEnv, "prod.example.test/path")
	_, query, _ = f.start(t, scope(dcr.ID), "")
	if query.Get("redirect_uri") != catalogAppOrigin+connectorOAuthCallbackPath {
		t.Fatalf("malformed override redirect_uri = %q", query.Get("redirect_uri"))
	}
	// Removing the override promotes the kept registration back.
	if registrations, _, _, _ := f.provider.counts(); registrations != 2 || query.Get("client_id") != "dcr-client-1" {
		t.Fatalf("after removing the override: registrations=%d client=%q", registrations, query.Get("client_id"))
	}
}

// The DCR client_name is configurable; a different name registers a new
// client (a registration is reused only for the same redirect URI and name)
// and the replaced one is kept for the tokens it issued.
func TestConnectorOAuthClientNameRegistration(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	dcr := f.create(t, f.dcr)
	f.offer(t, dcr.ID)
	f.grant(t, contextcap.ScopePerson, catalogTestStaff)
	scope := f.scope(dcr.ID, contextcap.ScopePerson, catalogTestStaff)
	clientName := func() string {
		f.provider.mu.Lock()
		defer f.provider.mu.Unlock()
		return f.provider.lastClientName
	}

	_, query, _ := f.start(t, scope, "")
	if clientName() != "Multica" || query.Get("client_id") != "dcr-client-1" {
		t.Fatalf("default registration: name=%q client=%q", clientName(), query.Get("client_id"))
	}
	_, query, _ = f.start(t, scope, "")
	if registrations, _, _, _ := f.provider.counts(); registrations != 1 || query.Get("client_id") != "dcr-client-1" {
		t.Fatalf("unchanged name re-registered: %d %q", registrations, query.Get("client_id"))
	}
	// The registration records its name; one stored before names were
	// recorded counts as "Multica", the name every earlier one was made with.
	record, err := f.h.loadConnectorOAuthClient(ctx, testWorkspaceID, dcr.ID)
	if err != nil || record.ClientName != "Multica" {
		t.Fatalf("stored name = %q %v", record.ClientName, err)
	}
	if legacy := (connectorOAuthRegistration{RedirectURI: "https://x.example.test/cb"}); !legacy.serves("https://x.example.test/cb", "Multica") ||
		legacy.serves("https://x.example.test/cb", "QwenTagPre") {
		t.Fatal("an unnamed registration must serve exactly the default name")
	}

	t.Setenv(connectorOAuthClientNameEnv, "QwenTagPre")
	_, query, _ = f.start(t, scope, "")
	if registrations, _, _, _ := f.provider.counts(); registrations != 2 || clientName() != "QwenTagPre" || query.Get("client_id") != "dcr-client-2" {
		t.Fatalf("renamed: registrations=%d name=%q client=%q", registrations, clientName(), query.Get("client_id"))
	}
	record, err = f.h.loadConnectorOAuthClient(ctx, testWorkspaceID, dcr.ID)
	if err != nil || record.ClientName != "QwenTagPre" || len(record.Previous) != 1 || record.Previous[0].Registration.ClientID != "dcr-client-1" ||
		record.Previous[0].registeredName() != "Multica" {
		t.Fatalf("record after renaming = %+v %v", record, err)
	}
	// Tokens of the earlier client still refresh with it.
	if registration, ok := record.registrationFor("dcr-client-1"); !ok || registration.Registration.ClientID != "dcr-client-1" {
		t.Fatalf("earlier client lookup = %+v %v", registration, ok)
	}
	// The name changes back: the kept registration is promoted, no new one.
	t.Setenv(connectorOAuthClientNameEnv, "")
	_, query, _ = f.start(t, scope, "")
	if registrations, _, _, _ := f.provider.counts(); registrations != 2 || query.Get("client_id") != "dcr-client-1" {
		t.Fatalf("name changed back: registrations=%d client=%q", registrations, query.Get("client_id"))
	}
}

// A 1:1 chat scene connects its person's account: the state stores the
// person scope, the callback re-checks the dm request, and a manager who is
// not that person cannot connect it.
func TestContextConfigConnectionDMSceneConnectsThePerson(t *testing.T) {
	f := newCatalogFixture(t)
	f.cleanupAppScenes(t)
	router := catalogAPIRouter(f.h)
	ctx := context.Background()
	const dmKey, doraStaff = "cidCatalogDora==", "staff-catalog-dora"
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM context_config_link WHERE agent_id = $1`, f.agentID)
	})
	dcr := f.storeTools(t, f.create(t, f.dcr))
	f.offer(t, dcr.ID)
	if err := contextcap.RegisterDirectScene(ctx, testPool, testWorkspaceID, f.agentID, catalogTestOrg, dmKey, "Dora"); err != nil {
		t.Fatal(err)
	}
	startPath := "/api/context-capabilities/agents/" + f.agentID + "/connections/start"
	start := func(userID string) *httptest.ResponseRecorder {
		return ctxcapMobile(t, router, http.MethodPost, startPath, userID, map[string]string{
			"scope_type": contextcap.ScopeScene, "scope_key": dmKey, "connector_id": dcr.ID,
		})
	}
	// The workspace owner manages the agent, but nobody knows whose DM it is.
	if rec := start(testUserID); rec.Code != http.StatusConflict || catalogErrorCode(t, rec) != contextCapErrDMPersonUnknown {
		t.Fatalf("manager, DM of an unknown person: %d %s", rec.Code, rec.Body.String())
	}

	// Dora redeems the personal link minted in the DM (person and DM scene
	// grants, as RedeemContextConfigLink writes them).
	dora := uuid.NewString()
	link, err := contextcap.InsertLink(ctx, testPool, contextcap.Link{
		TokenHash: contextcap.HashLinkToken(uuid.NewString()), WorkspaceID: testWorkspaceID, AgentID: f.agentID,
		ScopeType: contextcap.ScopePerson, OrgID: catalogTestOrg, ScopeKey: doraStaff, ScopeTitle: "Dora", ExtraSceneKey: dmKey,
	}, contextcap.LinkTTLPerson)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contextcap.RedeemLink(ctx, testPool, link.TokenHash, dora); err != nil {
		t.Fatal(err)
	}
	for _, grant := range []contextcap.Grant{
		{ScopeType: contextcap.ScopePerson, ScopeKey: doraStaff},
		{ScopeType: contextcap.ScopeScene, ScopeKey: dmKey},
	} {
		grant.UserID, grant.WorkspaceID, grant.AgentID, grant.OrgID, grant.ScopeTitle, grant.Source = dora, testWorkspaceID, f.agentID, catalogTestOrg, "Dora", contextcap.GrantSourceAgentLink
		if _, err := contextcap.UpsertGrant(ctx, testPool, grant, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if rec := start(testUserID); rec.Code != http.StatusForbidden || catalogErrorCode(t, rec) != contextCapErrPersonOnly {
		t.Fatalf("manager, Dora's DM: %d %s", rec.Code, rec.Body.String())
	}

	query, cookies := f.takeAuthorizeURL(t, start(dora))
	var scopeType, scopeKey string
	if err := testPool.QueryRow(ctx, `SELECT scope_type, scope_key FROM connector_oauth_state WHERE state_hash = $1`,
		hashConnectorOAuthState(query.Get("state"))).Scan(&scopeType, &scopeKey); err != nil || scopeType != contextcap.ScopePerson || scopeKey != doraStaff {
		t.Fatalf("stored state scope = %q %q %v", scopeType, scopeKey, err)
	}
	rec := browserGet(router, ConnectorOAuthCallbackPath+"?code=good-code&state="+url.QueryEscape(query.Get("state")), cookies...)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "connected="+url.QueryEscape(f.dcr.Slug)) {
		t.Fatalf("Dora's DM connect: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	personKey := contextcap.CredentialBinding{WorkspaceID: testWorkspaceID, AgentID: f.agentID, ConnectorID: dcr.ID,
		ScopeType: contextcap.ScopePerson, OrgID: catalogTestOrg, ScopeKey: doraStaff}
	if _, err := contextcap.GetCredential(ctx, testPool, personKey); err != nil {
		t.Fatalf("Dora's account not in her personal scope: %v", err)
	}
	sceneKey := personKey
	sceneKey.ScopeType, sceneKey.ScopeKey = contextcap.ScopeScene, dmKey
	if _, err := contextcap.GetCredential(ctx, testPool, sceneKey); err == nil {
		t.Fatal("Dora's account stored on the DM scene key")
	}
	bindings, err := contextcap.ListScopeBindings(ctx, testPool, testWorkspaceID, f.agentID, contextcap.ScopePerson, catalogTestOrg, doraStaff)
	if err != nil || len(bindings) != 1 || !bindings[0].Enabled || bindings[0].ResourceID != dcr.ID {
		t.Fatalf("Dora's personal binding = %+v %v", bindings, err)
	}

	// The callback re-checks the DM request: once Dora's grants are gone,
	// a connect she started is refused.
	query, cookies = f.takeAuthorizeURL(t, start(dora))
	if _, err := testPool.Exec(ctx, `DELETE FROM context_config_grant WHERE agent_id = $1 AND user_id = $2`, f.agentID, dora); err != nil {
		t.Fatal(err)
	}
	rec = browserGet(router, ConnectorOAuthCallbackPath+"?code=good-code&state="+url.QueryEscape(query.Get("state")), cookies...)
	if rec.Code != http.StatusFound || !strings.HasSuffix(rec.Header().Get("Location"), "connect_error="+connectOAuthErrForbidden) {
		t.Fatalf("callback after Dora lost her grants: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}
