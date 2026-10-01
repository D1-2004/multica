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
	forwardPreOrigin  = "https://pre-prod.example.test"
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
	// A pre-release origin is "https://pre-" + the production host.
	for origin, want := range map[string]string{
		"https://pre-fde-workbench.dingtalk.com": "https://fde-workbench.dingtalk.com",
		"https://Pre-Prod.example.test/":         "https://prod.example.test",
		"https://pre-prod.example.test:8443":     "https://prod.example.test:8443",
	} {
		if got, pre := connectorOAuthProductionOrigin(origin); !pre || got != want {
			t.Errorf("connectorOAuthProductionOrigin(%q) = %q %v, want %q", origin, got, pre, want)
		}
	}
	for _, origin := range []string{
		"https://fde-workbench.dingtalk.com", "http://pre-prod.example.test", "https://pre-localhost", "https://prep-prod.example.test",
		"https://x.pre-prod.example.test", "http://localhost:3000", "", "not an origin",
		// An apex or public-suffix production host: "pre-" + it is registrable
		// by anyone.
		"https://pre-qwentag.com", "https://pre-foo.github.io", "https://pre-example.co.uk",
	} {
		if got, pre := connectorOAuthProductionOrigin(origin); pre {
			t.Errorf("connectorOAuthProductionOrigin(%q) = %q, want no production origin", origin, got)
		}
	}
	// The consent-screen name follows the deployment.
	for appURL, want := range map[string]string{
		"https://pre-fde-workbench.dingtalk.com": "QwenTagPre",
		"https://fde-workbench.dingtalk.com":     "QwenTag",
		"http://localhost:3000":                  "QwenTag",
	} {
		h := &Handler{cfg: Config{AppURL: appURL, FrontendOrigin: appURL}}
		if got := h.connectorOAuthClientName(); got != want {
			t.Errorf("client name on %s = %q, want %q", appURL, got, want)
		}
	}
}

// Production forwards a state that names its own pre-release origin
// ("https://pre-" + its host) to that origin's same callback path and raw
// query, before any local handling, on both callback routes; anything else
// is refused. No configuration is involved.
func TestConnectorOAuthCallbackForwarding(t *testing.T) {
	prod := &Handler{cfg: Config{AppURL: forwardProdOrigin, FrontendOrigin: forwardProdOrigin}}
	useConnectorOAuthForward(t, prod)
	pre := &Handler{cfg: Config{AppURL: forwardPreOrigin, FrontendOrigin: forwardPreOrigin, A2AForwardRegistrationSecret: connectorOAuthForwardTestSecret}}
	router := catalogAPIRouter(prod)

	for _, path := range []string{ConnectorOAuthCallbackPath, connectorOAuthGitHubCallback} {
		state := forwardState(forwardPreOrigin)
		// Production forwards only a connect its pre-release registered.
		if err := pre.registerConnectorOAuthForward(context.Background(), forwardProdOrigin, state, connectorOAuthForwardRegistration{
			HomeOrigin: forwardPreOrigin, WorkspaceID: testWorkspaceID, ConnectorID: uuid.NewString(), ScopeType: "workspace",
			ExpiresAtMs: time.Now().Add(10 * time.Minute).UnixMilli(),
		}); err != nil {
			t.Fatal(err)
		}
		rawQuery := "code=the+code&state=" + url.QueryEscape(state) + "&iss=https%3A%2F%2Fprovider.example"
		rec := browserGet(router, path+"?"+rawQuery)
		want := forwardPreOrigin + path + "?" + rawQuery
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != want || rec.Header().Get("Referrer-Policy") != "no-referrer" ||
			rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s forward: %d %q, want %q", path, rec.Code, rec.Header().Get("Location"), want)
		}
		for name, home := range map[string]string{
			"another origin":                   "https://evil.example.test",
			"another deployment's pre-release": "https://pre-evil.example.test",
			"own pre-release over http":        "http://pre-prod.example.test",
			"own pre-release with a port":      "https://pre-prod.example.test:8443",
			"a subdomain of the pre-release":   "https://x.pre-prod.example.test",
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
	// A deployment that is not the production of the named pre-release (here
	// a pre-release itself) refuses the state.
	rec := browserGet(catalogAPIRouter(pre), ConnectorOAuthCallbackPath+"?code=c&state="+url.QueryEscape(forwardState("https://pre-other.example.test")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("foreign pre-release state on a pre-release: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// The router's GitHub dispatch still sends such states to the connector
	// flow.
	if !IsConnectorOAuthCallback(httptest.NewRequest(http.MethodGet, connectorOAuthGitHubCallback+"?state="+url.QueryEscape(forwardState(forwardPreOrigin)), nil)) {
		t.Fatal("a state with a home origin is not a connector callback")
	}
}

// preReleaseOf is the pre-release origin of a production origin.
func preReleaseOf(origin string) string {
	return strings.Replace(origin, "https://", "https://pre-", 1)
}

// Pre-release sends providers the production callback: the redirect URI is
// on the production origin, the state names the pre-release, the binding
// cookie stays on the pre-release origin, and the connect completes there
// once production forwards the callback.
func TestConnectorOAuthCallbackOriginRoundTrip(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	// The fixture's own origins play production; a copy of the handler on
	// the "pre-" hosts is its pre-release (same database).
	prodRouter := catalogAPIRouter(f.h)
	// The pre-release registers each connect with production, which then
	// forwards that connect's callback (and nothing else).
	useConnectorOAuthForward(t, f.h)
	pre := *f.h
	pre.cfg.AppURL, pre.cfg.FrontendOrigin = preReleaseOf(catalogAppOrigin), preReleaseOf(catalogWebOrigin)
	preRouter := catalogAPIRouter(&pre)
	dcr := f.storeTools(t, f.create(t, f.dcr))
	gh := f.storeTools(t, f.create(t, f.gh))
	f.offer(t, dcr.ID, gh.ID)
	f.grant(t, contextcap.ScopePerson, catalogTestStaff)
	scope := func(connectorID string) connectorOAuthScope {
		return f.scope(connectorID, contextcap.ScopePerson, catalogTestStaff)
	}

	// Production uses its own callback and a state without a home origin.
	_, query, _ := f.start(t, scope(dcr.ID), "")
	if query.Get("redirect_uri") != catalogAppOrigin+connectorOAuthCallbackPath || strings.Contains(strings.TrimPrefix(query.Get("state"), connectorOAuthStatePrefix), ".") {
		t.Fatalf("production start = %v", query)
	}
	if registrations, _, _, _ := f.provider.counts(); registrations != 1 {
		t.Fatalf("registrations = %d", registrations)
	}

	for _, tc := range []struct {
		connector internalConnector
		path      string
		home      string
		via       string
	}{
		{dcr, connectorOAuthCallbackPath, pre.cfg.AppURL, connectorOAuthViaDCR},
		{gh, connectorOAuthGitHubCallback, pre.cfg.FrontendOrigin, connectorOAuthViaGitHub},
	} {
		started, err := pre.startConnectorOAuth(ctx, connectorOAuthStart{connectorOAuthScope: scope(tc.connector.ID)})
		if err != nil {
			t.Fatal(err)
		}
		query := f.provideAuthorizeURL(t, started.AuthorizeURL)
		state := query.Get("state")
		if home, ok := splitConnectorOAuthState(state); !ok || home != tc.home {
			t.Fatalf("%s state %q home = %q %v", tc.via, state, home, ok)
		}
		production, _ := connectorOAuthProductionOrigin(tc.home)
		if query.Get("redirect_uri") != production+tc.path {
			t.Fatalf("%s redirect_uri = %q", tc.via, query.Get("redirect_uri"))
		}
		cookie := started.Cookie
		if cookie.Path != tc.path || !cookie.Secure || cookie.Name != connectorOAuthCookieName(hashConnectorOAuthState(state)) {
			t.Fatalf("%s cookie = %+v", tc.via, cookie)
		}
		// The provider returns to production, which forwards the callback
		// home; the browser there still has the binding cookie, and the code
		// is exchanged with the redirect URI the authorization used.
		rawQuery := "code=good-code&state=" + url.QueryEscape(state)
		forwarded := browserGet(prodRouter, tc.path+"?"+rawQuery)
		if forwarded.Code != http.StatusFound || forwarded.Header().Get("Location") != tc.home+tc.path+"?"+rawQuery {
			t.Fatalf("%s forward: %d %q", tc.via, forwarded.Code, forwarded.Header().Get("Location"))
		}
		location, err := url.Parse(forwarded.Header().Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		done := browserGet(preRouter, location.RequestURI(), cookie)
		if done.Code != http.StatusFound || !strings.Contains(done.Header().Get("Location"), "connected="+url.QueryEscape(tc.connector.CatalogSlug)) {
			t.Fatalf("%s completion at home: %d %q", tc.via, done.Code, done.Header().Get("Location"))
		}
	}
	if _, err := contextcap.GetCredential(ctx, testPool, f.personKey(dcr.ID)); err != nil {
		t.Fatalf("forwarded connect stored nothing: %v", err)
	}
	// Pre-release's redirect URI (and name) differ, so it registered its
	// own client, on the production callback.
	if registrations, _, _, _ := f.provider.counts(); registrations != 2 {
		t.Fatalf("registrations after a pre-release connect = %d", registrations)
	}
	f.provider.mu.Lock()
	registeredRedirect := f.provider.lastRegisteredRedirect
	f.provider.mu.Unlock()
	if registeredRedirect != catalogAppOrigin+connectorOAuthCallbackPath {
		t.Fatalf("registered redirect = %q", registeredRedirect)
	}
	// Back on production, its kept registration is promoted, not replaced.
	_, query, _ = f.start(t, scope(dcr.ID), "")
	if registrations, _, _, _ := f.provider.counts(); registrations != 2 || query.Get("client_id") != "dcr-client-1" {
		t.Fatalf("production again: registrations=%d client=%q", registrations, query.Get("client_id"))
	}
}

// The DCR client_name follows the deployment (QwenTag, QwenTagPre on
// pre-release); a different name registers a new client (a registration is
// reused only for the same redirect URI and name) and the replaced one is
// kept for the tokens it issued.
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
	if clientName() != "QwenTag" || query.Get("client_id") != "dcr-client-1" {
		t.Fatalf("production registration: name=%q client=%q", clientName(), query.Get("client_id"))
	}
	_, query, _ = f.start(t, scope, "")
	if registrations, _, _, _ := f.provider.counts(); registrations != 1 || query.Get("client_id") != "dcr-client-1" {
		t.Fatalf("unchanged name re-registered: %d %q", registrations, query.Get("client_id"))
	}
	// The registration records its name; one stored before names were
	// recorded counts as "Multica", the name every earlier one was made with.
	record, err := f.h.loadConnectorOAuthClient(ctx, testWorkspaceID, dcr.ID)
	if err != nil || record.ClientName != "QwenTag" {
		t.Fatalf("stored name = %q %v", record.ClientName, err)
	}
	if legacy := (connectorOAuthRegistration{RedirectURI: "https://x.example.test/cb"}); !legacy.serves("https://x.example.test/cb", "Multica") ||
		legacy.serves("https://x.example.test/cb", "QwenTag") {
		t.Fatal("an unnamed registration must serve exactly the legacy name")
	}

	useConnectorOAuthForward(t, f.h)
	pre := *f.h
	pre.cfg.AppURL, pre.cfg.FrontendOrigin = preReleaseOf(catalogAppOrigin), preReleaseOf(catalogWebOrigin)
	if _, err := pre.startConnectorOAuth(ctx, connectorOAuthStart{connectorOAuthScope: scope}); err != nil {
		t.Fatal(err)
	}
	if registrations, _, _, _ := f.provider.counts(); registrations != 2 || clientName() != "QwenTagPre" {
		t.Fatalf("pre-release: registrations=%d name=%q", registrations, clientName())
	}
	record, err = f.h.loadConnectorOAuthClient(ctx, testWorkspaceID, dcr.ID)
	if err != nil || record.ClientName != "QwenTagPre" || len(record.Previous) != 1 || record.Previous[0].Registration.ClientID != "dcr-client-1" ||
		record.Previous[0].registeredName() != "QwenTag" {
		t.Fatalf("record after the pre-release connect = %+v %v", record, err)
	}
	// Tokens of the earlier client still refresh with it.
	if registration, ok := record.registrationFor("dcr-client-1"); !ok || registration.Registration.ClientID != "dcr-client-1" {
		t.Fatalf("earlier client lookup = %+v %v", registration, ok)
	}
	// Production again: the kept registration is promoted, no new one.
	_, query, _ = f.start(t, scope, "")
	if registrations, _, _, _ := f.provider.counts(); registrations != 2 || query.Get("client_id") != "dcr-client-1" {
		t.Fatalf("production again: registrations=%d client=%q", registrations, query.Get("client_id"))
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
