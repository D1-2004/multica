package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/contextcap"
)

// catalogAPIRouter mirrors the official app wiring in cmd/server/router.go
// (the admin role check is covered by the router's RequireWorkspaceRole
// middleware and not repeated here).
func catalogAPIRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Get("/api/github/authorize", h.GitHubAuthorizeCallback)
	r.Get(ConnectorOAuthCallbackPath, h.ConnectorOAuthCallback)
	r.Get(ConnectorOAuthCallbackLegacyPath, h.ConnectorOAuthCallback)
	r.Route("/api/context-capabilities", func(r chi.Router) {
		r.Use(RequireDingTalkHumanActor)
		r.Get("/agents/{agentId}", h.GetContextConfigAgent)
		r.Put("/agents/{agentId}/credentials", h.PutContextConfigCredential)
		r.Post("/agents/{agentId}/connections/start", h.StartContextConfigConnection)
	})
	r.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.Use(RequireWorkspaceMCPHumanIssuer)
		r.Get("/internal-connectors", h.ListInternalConnectors)
		r.Patch("/internal-connectors/{connectorId}", h.UpdateInternalConnector)
		r.Post("/internal-connectors/{connectorId}/oauth/start", h.StartInternalConnectorOAuth)
		r.Post("/internal-connectors/{connectorId}/tools/refresh", h.RefreshInternalConnectorTools)
		r.Get("/connector-catalog", h.ListConnectorCatalog)
		r.Post("/connector-catalog/{slug}", h.AddCatalogConnector)
	})
	return r
}

// catalogAdmin issues a workspace admin request as userID. body may be nil,
// a raw string or a JSON value.
func catalogAdmin(t *testing.T, router http.Handler, method, path, userID string, body any, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	switch value := body.(type) {
	case nil:
		reader = bytes.NewReader(nil)
	case string:
		reader = bytes.NewReader([]byte(value))
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", userID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// browserGet follows nothing: it returns the raw answer to a browser GET
// that carries cookies.
func browserGet(router http.Handler, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for _, cookie := range cookies {
		req.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// takeAuthorizeURL decodes {"authorize_url"} and hands the PKCE challenge
// and redirect URI to the fake provider, as the browser would. It returns
// the authorize URL query and the cookies the start response set.
func (f *catalogFixture) takeAuthorizeURL(t *testing.T, rec *httptest.ResponseRecorder) (url.Values, []*http.Cookie) {
	t.Helper()
	ctxcapExpectStatus(t, rec, http.StatusOK, "start connection")
	var body struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	ctxcapDecode(t, rec, &body)
	parsed, err := url.Parse(body.AuthorizeURL)
	if err != nil || parsed.Scheme != "https" {
		t.Fatalf("authorize_url = %q", body.AuthorizeURL)
	}
	query := parsed.Query()
	f.provider.mu.Lock()
	f.provider.challenge = query.Get("code_challenge")
	f.provider.redirectURI = query.Get("redirect_uri")
	f.provider.mu.Unlock()
	return query, rec.Result().Cookies()
}

// assertBindingCookie checks the browser binding cookie a start response
// set for state: HttpOnly, SameSite=Lax, Secure on an https origin, scoped
// to the callback path. A DCR connect also sets the same binding on the
// legacy callback path, because cookie path matching does not treat
// /api/connectors/oauth/callback and /api/connector-oauth/callback as one.
func assertBindingCookie(t *testing.T, cookies []*http.Cookie, state, path string) {
	t.Helper()
	check := func(cookie *http.Cookie) {
		t.Helper()
		if cookie == nil || cookie.Name != connectorOAuthCookieName(hashConnectorOAuthState(state)) || cookie.Value == "" ||
			!cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != int(connectorOAuthStateTTL.Seconds()) {
			t.Fatalf("binding cookie = %+v", cookie)
		}
	}
	var match *http.Cookie
	for _, cookie := range cookies {
		if cookie.Path == path {
			match = cookie
			break
		}
	}
	check(match)
	if path != ConnectorOAuthCallbackPath {
		if len(cookies) != 1 {
			t.Fatalf("start cookies = %v", cookies)
		}
		return
	}
	if len(cookies) != 2 {
		t.Fatalf("start cookies = %v", cookies)
	}
	var legacy *http.Cookie
	for _, cookie := range cookies {
		if cookie.Path == connectorOAuthCallbackLegacyPath {
			legacy = cookie
		}
	}
	check(legacy)
	if legacy.Value != match.Value || legacy.Name != match.Name {
		t.Fatalf("legacy binding cookie = %+v, canonical = %+v", legacy, match)
	}
}

// assertInvalidConnectionPage checks the page an unknown, expired or
// replayed connect gets instead of a JSON body.
func assertInvalidConnectionPage(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusBadRequest || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(rec.Body.String(), "连接已失效") || !strings.Contains(rec.Body.String(), `href="`+catalogAppOrigin+`/dingtalk/configure"`) {
		t.Fatalf("invalid connection page: %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

func catalogErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	ctxcapDecode(t, rec, &body)
	return body.Code
}

func catalogWorkspaceSlug(t *testing.T) string {
	t.Helper()
	var slug string
	if err := testPool.QueryRow(context.Background(), `SELECT slug FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&slug); err != nil {
		t.Fatal(err)
	}
	return slug
}

// uuidPattern matches the ids in a response; "acc-" can occur inside a
// random UUID ("…2acc-…"), so ids are removed before looking for tokens.
var uuidPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func assertNoTokenMaterial(t *testing.T, what, body string) {
	t.Helper()
	body = uuidPattern.ReplaceAllString(body, "")
	for _, secret := range []string{"acc-", "ref-", "pat-", "gh-secret", "verifier"} {
		if strings.Contains(body, secret) {
			t.Fatalf("%s leaks %q: %s", what, secret, body)
		}
	}
}

func TestCatalogAPIAdminGalleryGitHubConnectAndTools(t *testing.T) {
	f := newCatalogFixture(t)
	t.Setenv("GITHUB_APP_SLUG", "multica-test-app")
	router := catalogAPIRouter(f.h)
	base := "/api/workspaces/" + testWorkspaceID

	type galleryApp struct {
		Slug           string  `json:"slug"`
		Name           string  `json:"name"`
		MCPURL         string  `json:"mcp_url"`
		AuthKind       string  `json:"auth_kind"`
		AllowsPAT      bool    `json:"allows_pat"`
		OAuthAvailable bool    `json:"oauth_available"`
		ConnectorID    *string `json:"connector_id"`
		InstallURL     *string `json:"install_url"`
	}
	gallery := func() map[string]galleryApp {
		t.Helper()
		rec := catalogAdmin(t, router, http.MethodGet, base+"/connector-catalog", testUserID, nil)
		ctxcapExpectStatus(t, rec, http.StatusOK, "catalog list")
		var body struct {
			Apps []galleryApp `json:"apps"`
		}
		ctxcapDecode(t, rec, &body)
		out := map[string]galleryApp{}
		for _, app := range body.Apps {
			out[app.Slug] = app
		}
		return out
	}
	apps := gallery()
	gh, dcr := apps[f.gh.Slug], apps[f.dcr.Slug]
	if gh.Name != "GitHub Test" || gh.MCPURL != f.gh.MCPURL || gh.AuthKind != "oauth_github_app" || !gh.AllowsPAT || !gh.OAuthAvailable ||
		gh.ConnectorID != nil || gh.InstallURL == nil || *gh.InstallURL != "https://github.com/apps/multica-test-app/installations/new" {
		t.Fatalf("github gallery entry = %+v", gh)
	}
	if dcr.AuthKind != "oauth_dcr" || dcr.AllowsPAT || !dcr.OAuthAvailable || dcr.ConnectorID != nil || dcr.InstallURL != nil {
		t.Fatalf("dcr gallery entry = %+v", dcr)
	}

	// Agent-issued tokens cannot manage the library.
	if rec := catalogAdmin(t, router, http.MethodGet, base+"/connector-catalog", testUserID, nil, "X-Actor-Source", "task_token"); rec.Code != http.StatusForbidden {
		t.Fatalf("agent actor listed the catalog: %d", rec.Code)
	}

	// Add: 201 once, then 200 with the same connector; unknown slug 404.
	type addResponse struct {
		Connector map[string]any `json:"connector"`
	}
	rec := catalogAdmin(t, router, http.MethodPost, base+"/connector-catalog/"+f.gh.Slug, testUserID, nil)
	ctxcapExpectStatus(t, rec, http.StatusCreated, "add github")
	var added addResponse
	ctxcapDecode(t, rec, &added)
	connectorID, _ := added.Connector["id"].(string)
	if connectorID == "" || added.Connector["catalog_slug"] != f.gh.Slug || added.Connector["auth_mode"] != "oauth" || added.Connector["enabled"] != true ||
		added.Connector["upstream_url"] != f.gh.MCPURL || added.Connector["write_enabled"] != false || added.Connector["discovered_tool_count"] != float64(0) ||
		added.Connector["credential_account"] != "" {
		t.Fatalf("added connector = %v", added.Connector)
	}
	if tools, ok := added.Connector["allowed_tools"].([]any); !ok || len(tools) != 0 {
		t.Fatalf("new catalog connector tools = %v", added.Connector["allowed_tools"])
	}
	rec = catalogAdmin(t, router, http.MethodPost, base+"/connector-catalog/"+f.gh.Slug, testUserID, nil)
	ctxcapExpectStatus(t, rec, http.StatusOK, "add github again")
	var again addResponse
	ctxcapDecode(t, rec, &again)
	if again.Connector["id"] != connectorID {
		t.Fatalf("second add returned %v, want %s", again.Connector["id"], connectorID)
	}
	ctxcapExpectStatus(t, catalogAdmin(t, router, http.MethodPost, base+"/connector-catalog/no-such-app", testUserID, nil), http.StatusNotFound, "unknown app")
	if got := gallery()[f.gh.Slug].ConnectorID; got == nil || *got != connectorID {
		t.Fatalf("gallery connector_id = %v, want %s", got, connectorID)
	}

	// Tools cannot be refreshed before an account is connected.
	rec = catalogAdmin(t, router, http.MethodPost, base+"/internal-connectors/"+connectorID+"/tools/refresh", testUserID, nil)
	if rec.Code != http.StatusConflict || catalogErrorCode(t, rec) != "no_connected_account" {
		t.Fatalf("refresh without an account: %d %s", rec.Code, rec.Body.String())
	}

	// OAuth start: body is optional and strict; return_to must stay on the
	// app origin; only workspace owners/admins may connect the shared account.
	startPath := base + "/internal-connectors/" + connectorID + "/oauth/start"
	ctxcapExpectStatus(t, catalogAdmin(t, router, http.MethodPost, startPath, testUserID, `{"return_to":"/x","extra":1}`), http.StatusBadRequest, "unknown start field")
	rec = catalogAdmin(t, router, http.MethodPost, startPath, testUserID, map[string]string{"return_to": "https://evil.example/steal"})
	if rec.Code != http.StatusBadRequest || catalogErrorCode(t, rec) != "invalid_return_to" {
		t.Fatalf("foreign return_to: %d %s", rec.Code, rec.Body.String())
	}
	rec = catalogAdmin(t, router, http.MethodPost, startPath, uuid.NewString(), nil)
	if rec.Code != http.StatusForbidden || catalogErrorCode(t, rec) != connectOAuthErrForbidden {
		t.Fatalf("non-member start: %d %s", rec.Code, rec.Body.String())
	}
	ctxcapExpectStatus(t, catalogAdmin(t, router, http.MethodPost, base+"/internal-connectors/"+uuid.NewString()+"/oauth/start", testUserID, nil), http.StatusNotFound, "unknown connector start")
	ctxcapExpectStatus(t, catalogAdmin(t, router, http.MethodPost, startPath, testUserID, nil), http.StatusOK, "start without a body")
	query, cookies := f.takeAuthorizeURL(t, catalogAdmin(t, router, http.MethodPost, startPath, testUserID, map[string]string{}))
	state := query.Get("state")
	if query.Get("client_id") != "gh-client" || query.Get("redirect_uri") != catalogWebOrigin+"/api/github/authorize" || !isConnectorOAuthState(state) {
		t.Fatalf("GitHub authorize query = %v", query)
	}
	assertBindingCookie(t, cookies, state, "/api/github/authorize")

	// The shared GitHub App callback delegates "mcpc." states and never
	// touches the install cookie; it clears the state's binding cookie.
	rec = browserGet(router, "/api/github/authorize?code=good-code&state="+url.QueryEscape(state), cookies...)
	wantRedirect := catalogAppOrigin + "/" + url.PathEscape(catalogWorkspaceSlug(t)) + "/internal-connectors?connected=" + f.gh.Slug
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != wantRedirect {
		t.Fatalf("GitHub connector callback: %d %q, want %q", rec.Code, rec.Header().Get("Location"), wantRedirect)
	}
	if set := rec.Result().Cookies(); len(set) != 1 || set[0].Name != cookies[0].Name || set[0].MaxAge >= 0 || set[0].Path != "/api/github/authorize" {
		t.Fatalf("GitHub connector callback cookies = %v", set)
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("GitHub connector callback headers = %v", rec.Header())
	}
	// Replay has no trusted destination.
	assertInvalidConnectionPage(t, browserGet(router, "/api/github/authorize?code=good-code&state="+url.QueryEscape(state), cookies...))
	// Any other state keeps the GitHub App install flow.
	rec = browserGet(router, "/api/github/authorize?code=good-code&state=not-a-connector-state")
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "github_error=invalid_state") {
		t.Fatalf("install-flow state: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// The list shows the connected shared account and the pinned tools.
	item, body := listedConnector(t, f.h, connectorID)
	if item["credential_account"] != "@octo" || item["credential_ready"] != true || item["credential_source"] != "workspace" ||
		item["discovered_tool_count"] != float64(2) || item["write_enabled"] != false || item["catalog_slug"] != f.gh.Slug {
		t.Fatalf("listed connector after connect = %v", item)
	}
	assertNoTokenMaterial(t, "connector list", body)

	rec = catalogAdmin(t, router, http.MethodPost, base+"/internal-connectors/"+connectorID+"/tools/refresh", testUserID, nil)
	ctxcapExpectStatus(t, rec, http.StatusOK, "refresh tools")
	var refreshed struct {
		Discovered   int      `json:"discovered"`
		AllowedTools []string `json:"allowed_tools"`
	}
	ctxcapDecode(t, rec, &refreshed)
	if refreshed.Discovered != 2 || strings.Join(refreshed.AllowedTools, ",") != "search" {
		t.Fatalf("refresh = %+v", refreshed)
	}

	// PATCH write_enabled re-pins from the discovery snapshot.
	rec = catalogAdmin(t, router, http.MethodPatch, base+"/internal-connectors/"+connectorID, testUserID, map[string]any{
		"name": "GitHub", "upstream_url": f.gh.MCPURL, "auth_mode": "oauth", "allowed_tools": []string{}, "agent_ids": []string{},
		"enabled": true, "write_enabled": true,
	})
	ctxcapExpectStatus(t, rec, http.StatusOK, "enable writes")
	item, _ = listedConnector(t, f.h, connectorID)
	tools, _ := item["allowed_tools"].([]any)
	if item["write_enabled"] != true || len(tools) != 2 || tools[0] != "search" || tools[1] != "create_issue" {
		t.Fatalf("after write toggle = %v", item)
	}

	// There is no external-browser start: the body field is unknown, so the
	// request is refused before any state is stored or cookie set, and no
	// begin route answers.
	var statesBefore, statesAfter int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM connector_oauth_state WHERE connector_id = $1::uuid`, connectorID).Scan(&statesBefore); err != nil {
		t.Fatal(err)
	}
	rec = catalogAdmin(t, router, http.MethodPost, startPath, testUserID, map[string]any{"external_browser": true})
	if rec.Code != http.StatusBadRequest || len(rec.Result().Cookies()) != 0 || strings.Contains(rec.Body.String(), "authorize_url") {
		t.Fatalf("external_browser start: %d %s cookies %v", rec.Code, rec.Body.String(), rec.Result().Cookies())
	}
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM connector_oauth_state WHERE connector_id = $1::uuid`, connectorID).Scan(&statesAfter); err != nil {
		t.Fatal(err)
	}
	if statesAfter != statesBefore {
		t.Fatalf("external_browser start stored a state: %d -> %d", statesBefore, statesAfter)
	}

	// Custom connectors keep their creation-time tools.
	custom := uuid.NewString()
	if _, err := testPool.Exec(context.Background(), `INSERT INTO internal_connector (id, workspace_id, name, upstream_url, credential_ref, auth_mode, allowed_tools, enabled)
		VALUES ($1, $2, 'Custom', 'https://safe.example.test/mcp', $3, 'none', '["read"]'::jsonb, false)`, custom, testWorkspaceID, connectorCredentialRef(custom)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM internal_connector WHERE id = $1`, custom)
	})
	rec = catalogAdmin(t, router, http.MethodPost, base+"/internal-connectors/"+custom+"/tools/refresh", testUserID, nil)
	if rec.Code != http.StatusBadRequest || catalogErrorCode(t, rec) != "not_official_app" {
		t.Fatalf("custom refresh: %d %s", rec.Code, rec.Body.String())
	}
	rec = catalogAdmin(t, router, http.MethodPost, base+"/internal-connectors/"+custom+"/oauth/start", testUserID, nil)
	if rec.Code != http.StatusBadRequest || catalogErrorCode(t, rec) != "not_oauth" {
		t.Fatalf("custom OAuth start: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCatalogAPIMobileConnectionStartCallbackAndDetail(t *testing.T) {
	f := newCatalogFixture(t)
	t.Setenv("GITHUB_APP_SLUG", "multica-test-app")
	router := catalogAPIRouter(f.h)
	dcr := f.create(t, f.dcr)
	gh := f.create(t, f.gh)
	startPath := "/api/context-capabilities/agents/" + f.agentID + "/connections/start"
	start := func(userID, scopeType, key, connectorID string, extra map[string]string) *httptest.ResponseRecorder {
		body := map[string]string{"scope_type": scopeType, "scope_key": key, "connector_id": connectorID}
		for k, v := range extra {
			body[k] = v
		}
		return ctxcapMobile(t, router, http.MethodPost, startPath, userID, body)
	}

	// Authority: a live grant for exactly that scope, then the offer rule.
	ctxcapExpectStatus(t, start(testUserID, contextcap.ScopePerson, catalogTestStaff, dcr.ID, nil), http.StatusForbidden, "person without grant")
	f.grant(t, contextcap.ScopePerson, catalogTestStaff)
	f.grant(t, contextcap.ScopeScene, catalogTestScene)
	rec := start(testUserID, contextcap.ScopePerson, catalogTestStaff, dcr.ID, nil)
	if rec.Code != http.StatusForbidden || catalogErrorCode(t, rec) != connectOAuthErrForbidden {
		t.Fatalf("person start, connector neither offered nor granted: %d %s", rec.Code, rec.Body.String())
	}
	ctxcapExpectStatus(t, start(testUserID, contextcap.ScopePerson, "someone-else", dcr.ID, nil), http.StatusForbidden, "another person's scope")
	ctxcapExpectStatus(t, start(testUserID, contextcap.ScopePerson, catalogTestStaff, uuid.NewString(), nil), http.StatusForbidden, "unknown connector")
	ctxcapExpectStatus(t, start(testUserID, contextcap.ScopePerson, catalogTestStaff, "not-a-uuid", nil), http.StatusBadRequest, "malformed connector id")
	ctxcapExpectStatus(t, start(testUserID, contextcap.ScopePerson, catalogTestStaff, dcr.ID, map[string]string{"extra": "x"}), http.StatusBadRequest, "unknown body field")
	ctxcapExpectStatus(t, start(testUserID, contextcap.ScopeScene, catalogTestScene, dcr.ID, nil), http.StatusForbidden, "scene start, connector neither offered nor granted")
	f.grantGlobally(t, dcr.ID)
	f.offer(t, dcr.ID, gh.ID)
	f.takeAuthorizeURL(t, start(testUserID, contextcap.ScopeScene, catalogTestScene, dcr.ID, nil))
	rec = start(testUserID, contextcap.ScopePerson, catalogTestStaff, dcr.ID, map[string]string{"external_browser": "true"})
	if rec.Code != http.StatusBadRequest || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("mobile external_browser start: %d %s cookies %v", rec.Code, rec.Body.String(), rec.Result().Cookies())
	}
	rec = start(testUserID, contextcap.ScopePerson, catalogTestStaff, dcr.ID, map[string]string{"return_to": "//evil.example/"})
	if rec.Code != http.StatusBadRequest || catalogErrorCode(t, rec) != "invalid_return_to" {
		t.Fatalf("mobile foreign return_to: %d %s", rec.Code, rec.Body.String())
	}
	// Requests without a DingTalk session never reach the handler.
	req := httptest.NewRequest(http.MethodPost, startPath, strings.NewReader(`{}`))
	req.Header.Set("X-User-ID", testUserID)
	noSession := httptest.NewRecorder()
	router.ServeHTTP(noSession, req)
	if noSession.Code == http.StatusOK {
		t.Fatal("connection start accepted a non-DingTalk session")
	}

	// Phishing: a grant holder forwards the provider URL of their own
	// person connect. The colleague's browser has no binding cookie, so the
	// callback refuses it and stores nothing.
	query, cookies := f.takeAuthorizeURL(t, start(testUserID, contextcap.ScopePerson, catalogTestStaff, dcr.ID, nil))
	assertBindingCookie(t, cookies, query.Get("state"), ConnectorOAuthCallbackPath)
	rec = browserGet(router, ConnectorOAuthCallbackPath+"?code=good-code&state="+url.QueryEscape(query.Get("state")))
	if rec.Code != http.StatusFound || !strings.HasSuffix(rec.Header().Get("Location"), "connect_error="+connectOAuthErrBrowserMismatch) {
		t.Fatalf("callback from another browser: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if _, err := contextcap.GetCredential(context.Background(), testPool, f.personKey(dcr.ID)); err == nil {
		t.Fatal("credential stored from a browser that did not start the connect")
	}

	// Person connect through the DCR callback route.
	query, cookies = f.takeAuthorizeURL(t, start(testUserID, contextcap.ScopePerson, catalogTestStaff, dcr.ID, nil))
	if query.Get("redirect_uri") != catalogAppOrigin+ConnectorOAuthCallbackPath {
		t.Fatalf("DCR redirect_uri = %q", query.Get("redirect_uri"))
	}
	rec = browserGet(router, ConnectorOAuthCallbackPath+"?code=good-code&state="+url.QueryEscape(query.Get("state")), cookies...)
	wantRedirect := catalogAppOrigin + "/dingtalk/configure?agent=" + url.QueryEscape(f.agentID) + "&connected=" + f.dcr.Slug
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != wantRedirect || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("DCR callback: %d %q, want %q", rec.Code, rec.Header().Get("Location"), wantRedirect)
	}
	assertInvalidConnectionPage(t, browserGet(router, ConnectorOAuthCallbackPath+"?code=good-code&state="+url.QueryEscape(query.Get("state")), cookies...))
	assertInvalidConnectionPage(t, browserGet(router, ConnectorOAuthCallbackPath+"?code=good-code&state=mcpc.unknown"))
	// A provider denial and an oversized code both end on the page with an error.
	query, cookies = f.takeAuthorizeURL(t, start(testUserID, contextcap.ScopePerson, catalogTestStaff, dcr.ID, nil))
	rec = browserGet(router, ConnectorOAuthCallbackPath+"?error=access_denied&state="+url.QueryEscape(query.Get("state")), cookies...)
	if rec.Code != http.StatusFound || !strings.HasSuffix(rec.Header().Get("Location"), "connect_error=access_denied") {
		t.Fatalf("denied callback: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	query, cookies = f.takeAuthorizeURL(t, start(testUserID, contextcap.ScopePerson, catalogTestStaff, dcr.ID, nil))
	rec = browserGet(router, ConnectorOAuthCallbackPath+"?code="+strings.Repeat("c", connectorOAuthMaxCode+1)+"&state="+url.QueryEscape(query.Get("state")), cookies...)
	if rec.Code != http.StatusFound || !strings.HasSuffix(rec.Header().Get("Location"), "connect_error="+connectOAuthErrProviderError) {
		t.Fatalf("oversized code callback: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// A GitHub App state is refused on the DCR route (and the reverse).
	query, cookies = f.takeAuthorizeURL(t, start(testUserID, contextcap.ScopePerson, catalogTestStaff, gh.ID, nil))
	rec = browserGet(router, ConnectorOAuthCallbackPath+"?code=good-code&state="+url.QueryEscape(query.Get("state")), cookies...)
	if rec.Code != http.StatusFound || !strings.HasSuffix(rec.Header().Get("Location"), "connect_error="+connectOAuthErrInvalidState) {
		t.Fatalf("GitHub state on the DCR route: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// A person pastes a GitHub Personal Access Token: accepted for the
	// OAuth app that allows PATs, first discovery pins its tools. A DCR app
	// takes no pasted token.
	f.addValidToken("pat-mobile-token")
	credentialsPath := "/api/context-capabilities/agents/" + f.agentID + "/credentials"
	rec = ctxcapMobile(t, router, http.MethodPut, credentialsPath, testUserID, map[string]string{
		"scope_type": contextcap.ScopePerson, "scope_key": catalogTestStaff, "connector_id": gh.ID, "bearer": "pat-mobile-token",
	})
	ctxcapExpectStatus(t, rec, http.StatusOK, "PAT for GitHub")
	var put struct {
		Credential contextCapCredentialDTO `json:"credential"`
	}
	ctxcapDecode(t, rec, &put)
	if put.Credential.Kind != contextcap.CredentialKindBearer || !strings.HasPrefix(put.Credential.Hint, "••••") {
		t.Fatalf("PAT credential = %+v", put.Credential)
	}
	assertNoTokenMaterial(t, "PAT response", rec.Body.String())
	loaded, err := f.h.loadInternalConnector(context.Background(), testWorkspaceID, gh.ID)
	if err != nil || len(loaded.DiscoveredTools) != 2 || strings.Join(loaded.AllowedTools, ",") != "search" {
		t.Fatalf("GitHub tools after PAT = %+v %v", loaded.AllowedTools, err)
	}
	rec = ctxcapMobile(t, router, http.MethodPut, credentialsPath, testUserID, map[string]string{
		"scope_type": contextcap.ScopePerson, "scope_key": catalogTestStaff, "connector_id": dcr.ID, "bearer": "pat-other",
	})
	ctxcapExpectStatus(t, rec, http.StatusBadRequest, "pasted token for a DCR app")

	// GitHub also becomes a global connector with a workspace credential.
	f.grantGlobally(t, gh.ID)
	sealed, err := f.h.sealWorkspaceConnectorSecret(testWorkspaceID, gh.ID, contextcap.Secret{Bearer: "pat-workspace-token"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE internal_connector SET credential_ciphertext = $2 WHERE id = $1`, gh.ID, sealed); err != nil {
		t.Fatal(err)
	}

	rec = ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+f.agentID, testUserID, nil)
	ctxcapExpectStatus(t, rec, http.StatusOK, "agent detail")
	assertNoTokenMaterial(t, "agent detail", rec.Body.String())
	var detail struct {
		Global struct {
			Connectors []map[string]any `json:"connectors"`
		} `json:"global"`
		Offers struct {
			Connectors []map[string]any `json:"connectors"`
		} `json:"offers"`
		Person *struct {
			Credentials []map[string]any `json:"credentials"`
		} `json:"person"`
	}
	ctxcapDecode(t, rec, &detail)
	offered := map[string]map[string]any{}
	for _, c := range detail.Offers.Connectors {
		offered[c["id"].(string)] = c
	}
	if c := offered[dcr.ID]; c == nil || c["catalog_slug"] != f.dcr.Slug || c["auth_mode"] != "oauth" || c["accepts_pat"] != false ||
		c["accepts_credential"] != false || c["credential_required"] != true || c["install_url"] != nil || c["oauth_available"] != true {
		t.Fatalf("offered DCR connector = %v", c)
	}
	if c := offered[gh.ID]; c == nil || c["catalog_slug"] != f.gh.Slug || c["auth_mode"] != "oauth" || c["accepts_pat"] != true ||
		c["accepts_credential"] != true || c["credential_required"] != false || c["install_url"] != "https://github.com/apps/multica-test-app/installations/new" ||
		c["oauth_available"] != true {
		t.Fatalf("offered GitHub connector = %v", c)
	}
	// Both are granted (通用能力); the DCR app has no workspace account yet
	// and is listed all the same, since every scope may connect its own.
	global := map[string]any{}
	for _, c := range detail.Global.Connectors {
		global[c["id"].(string)] = c["catalog_slug"]
	}
	if len(global) != 2 || global[gh.ID] != f.gh.Slug || global[dcr.ID] != f.dcr.Slug {
		t.Fatalf("global connectors = %v", detail.Global.Connectors)
	}
	if detail.Person == nil {
		t.Fatal("person scope missing")
	}
	kinds := map[string]map[string]any{}
	for _, credential := range detail.Person.Credentials {
		kinds[credential["connector_id"].(string)] = credential
	}
	if c := kinds[dcr.ID]; c == nil || c["kind"] != contextcap.CredentialKindOAuth || c["hint"] != "@octo" {
		t.Fatalf("person DCR credential = %v", c)
	}
	if c := kinds[gh.ID]; c == nil || c["kind"] != contextcap.CredentialKindBearer {
		t.Fatalf("person GitHub credential = %v", c)
	}

	// Without the GitHub App client secret the server cannot run GitHub's
	// OAuth: the detail says so (the page then offers only the PAT form).
	t.Setenv("GITHUB_APP_CLIENT_SECRET", "")
	rec = ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+f.agentID, testUserID, nil)
	ctxcapExpectStatus(t, rec, http.StatusOK, "agent detail without GitHub OAuth")
	var withoutGitHubOAuth struct {
		Offers struct {
			Connectors []map[string]any `json:"connectors"`
		} `json:"offers"`
	}
	ctxcapDecode(t, rec, &withoutGitHubOAuth)
	if len(withoutGitHubOAuth.Offers.Connectors) != 2 {
		t.Fatalf("offered connectors = %v", withoutGitHubOAuth.Offers.Connectors)
	}
	for _, c := range withoutGitHubOAuth.Offers.Connectors {
		want := c["id"] == dcr.ID
		if c["oauth_available"] != want {
			t.Fatalf("oauth_available without the GitHub App secret = %v", c)
		}
	}
}
