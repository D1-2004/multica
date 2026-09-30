package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/connectorcatalog"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/remotemcp"
)

const (
	catalogTestOrg   = "org-catalog"
	catalogTestStaff = "staff-catalog-1"
	catalogTestScene = "cidCatalogScene=="
	catalogAppOrigin = "https://app.example.test"
	catalogWebOrigin = "https://web.example.test"
)

// fakeProvider is an official app on one TLS origin: a Streamable HTTP MCP
// endpoint that requires a session (/mcp) or not (/gh/mcp), protected
// resource and authorization server metadata, dynamic registration, a token
// endpoint that checks PKCE S256, and an account endpoint (/user).
type fakeProvider struct {
	*httptest.Server

	mu                sync.Mutex
	valid             map[string]bool
	sessions          map[string]bool
	challenge         string
	redirectURI       string
	registrations     int
	codeExchanges     int
	refreshRequests   int
	refreshMode       string // "" or "invalid_grant"
	refreshDelay      time.Duration
	nextToken         int
	initializes       int
	lastClientSecret  string
	lastResourceParam string
	lastRefreshClient string
	toolCalls         []string
	// lastClientName and lastRegisteredRedirect are the client_name and
	// first redirect URI of the last dynamic client registration.
	lastClientName         string
	lastRegisteredRedirect string
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	p := &fakeProvider{valid: map[string]bool{}, sessions: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": p.URL + "/mcp", "authorization_servers": []string{p.URL}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": p.URL, "authorization_endpoint": p.URL + "/authorize", "token_endpoint": p.URL + "/token",
			"registration_endpoint": p.URL + "/register", "code_challenge_methods_supported": []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"none"},
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ClientName   string   `json:"client_name"`
			RedirectURIs []string `json:"redirect_uris"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		p.registrations++
		n := p.registrations
		p.lastClientName = body.ClientName
		if len(body.RedirectURIs) > 0 {
			p.lastRegisteredRedirect = body.RedirectURIs[0]
		}
		p.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": fmt.Sprintf("dcr-client-%d", n), "token_endpoint_auth_method": "none"})
	})
	mux.HandleFunc("/token", p.token)
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		if !p.accepts(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "octo"})
	})
	mux.HandleFunc("/mcp", p.mcp(true))
	mux.HandleFunc("/gh/mcp", p.mcp(false))
	p.Server = httptest.NewTLSServer(mux)
	t.Cleanup(p.Close)
	return p
}

func (p *fakeProvider) host() string {
	u, _ := url.Parse(p.URL)
	return u.Host
}

func (p *fakeProvider) accepts(r *http.Request) bool {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.valid[token]
}

func (p *fakeProvider) counts() (registrations, exchanges, refreshes, initializes int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.registrations, p.codeExchanges, p.refreshRequests, p.initializes
}

func (p *fakeProvider) issueLocked() (string, string) {
	p.nextToken++
	access := fmt.Sprintf("acc-%d", p.nextToken)
	p.valid[access] = true
	return access, fmt.Sprintf("ref-%d", p.nextToken)
}

func (p *fakeProvider) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		p.mu.Lock()
		p.codeExchanges++
		p.lastClientSecret = r.Form.Get("client_secret")
		p.lastResourceParam = r.Form.Get("resource")
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		ok := r.Form.Get("code") == "good-code" && r.Form.Get("redirect_uri") == p.redirectURI &&
			base64.RawURLEncoding.EncodeToString(sum[:]) == p.challenge
		var access, refresh string
		if ok {
			access, refresh = p.issueLocked()
		}
		p.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "token_type": "bearer", "expires_in": 3600, "refresh_token": refresh})
	case "refresh_token":
		p.mu.Lock()
		p.refreshRequests++
		p.lastRefreshClient = r.Form.Get("client_id")
		delay, mode := p.refreshDelay, p.refreshMode
		p.mu.Unlock()
		time.Sleep(delay)
		if mode == "invalid_grant" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": "refresh token " + r.Form.Get("refresh_token") + " revoked"})
			return
		}
		p.mu.Lock()
		access, refresh := p.issueLocked()
		p.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "token_type": "bearer", "expires_in": 3600, "refresh_token": refresh})
	default:
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "unsupported_grant_type"})
	}
}

func (p *fakeProvider) mcp(requireSession bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !p.accepts(r) {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+p.URL+`/.well-known/oauth-protected-resource/mcp"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		write := func(result any) {
			w.Header().Set("Content-Type", "text/event-stream")
			payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
		}
		switch req.Method {
		case "initialize":
			p.mu.Lock()
			p.initializes++
			id := fmt.Sprintf("session-%d", p.initializes)
			p.sessions[id] = true
			p.mu.Unlock()
			w.Header().Set("Mcp-Session-Id", id)
			write(map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}})
			return
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		}
		p.mu.Lock()
		known := p.sessions[r.Header.Get("Mcp-Session-Id")]
		p.mu.Unlock()
		if requireSession && !known {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch req.Method {
		case "tools/list":
			write(map[string]any{"tools": []any{
				map[string]any{"name": "search", "description": "Search", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}},
				map[string]any{"name": "create_issue", "description": "Create", "inputSchema": map[string]any{"type": "object"}},
			}})
		case "tools/call":
			p.mu.Lock()
			p.toolCalls = append(p.toolCalls, req.Params.Name)
			p.mu.Unlock()
			write(map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok:" + req.Params.Name}}})
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}
}

// catalogFixture installs a test catalog (a DCR app and a GitHub App style
// app, both served by one fakeProvider), a handler and an agent with a
// DingTalk identity. Rows are removed in t.Cleanup.
type catalogFixture struct {
	h        *Handler
	box      *secretbox.Box
	provider *fakeProvider
	dcr      connectorcatalog.App
	gh       connectorcatalog.App
	agentID  string
}

func newCatalogFixture(t *testing.T) *catalogFixture {
	t.Helper()
	if testPool == nil {
		t.Skip("database not available")
	}
	t.Setenv("MULTICA_INTERNAL_MCP_ALLOWED_HOST_SUFFIXES", "safe.example.test")
	t.Setenv("GITHUB_APP_CLIENT_ID", "gh-client")
	t.Setenv("GITHUB_APP_CLIENT_SECRET", "gh-secret")
	provider := newFakeProvider(t)
	suffix := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	dcr := connectorcatalog.App{
		Slug: "demo-" + suffix, Name: "Demo", MCPURL: provider.URL + "/mcp", AuthKind: connectorcatalog.AuthOAuthDCR,
		Hosts: []string{provider.host()}, AccountURL: provider.URL + "/user",
	}
	gh := connectorcatalog.App{
		Slug: "gh-" + suffix, Name: "GitHub Test", MCPURL: provider.URL + "/gh/mcp", AuthKind: connectorcatalog.AuthOAuthGitHubApp,
		AllowsPAT: true, Hosts: []string{provider.host()}, AuthorizationEndpoint: provider.URL + "/login/oauth/authorize",
		TokenEndpoint: provider.URL + "/token", AccountURL: provider.URL + "/user",
	}
	catalog, err := connectorcatalog.New(dcr, gh)
	if err != nil {
		t.Fatal(err)
	}
	previousCatalog, previousClient := connectorCatalog, catalogExternalClient
	connectorCatalog = catalog
	transport := provider.Client().Transport
	catalogExternalClient = func(app connectorcatalog.App) *remotemcp.ExternalClient {
		return remotemcp.NewExternalClientWithTransport(app.Hosts, transport)
	}
	t.Cleanup(func() { connectorCatalog, catalogExternalClient = previousCatalog, previousClient })

	box, err := secretbox.New(bytes.Repeat([]byte("o"), secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	f := &catalogFixture{
		h: &Handler{
			Queries: db.New(testPool), DB: testPool, TxStarter: testPool, TaskService: testHandler.TaskService,
			InternalConnectorSecretBox: box, InternalConnectorRedis: &semanticaTestRates{n: 1},
			cfg: Config{PublicURL: "https://multica.example", AppURL: catalogAppOrigin, FrontendOrigin: catalogWebOrigin},
		},
		box: box, provider: provider, dcr: dcr, gh: gh, agentID: uuid.NewString(),
	}
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `INSERT INTO agent (id, workspace_id, name, runtime_mode, runtime_id, owner_id)
		VALUES ($1, $2, $3, 'cloud', $4, $5)`, f.agentID, testWorkspaceID, "Catalog agent "+f.agentID[:8], testRuntimeID, testUserID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		var connectorIDs []string
		rows, err := testPool.Query(bg, `SELECT id::text FROM internal_connector WHERE workspace_id = $1 AND catalog_slug = ANY($2::text[])`, testWorkspaceID, []string{dcr.Slug, gh.Slug})
		if err == nil {
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					connectorIDs = append(connectorIDs, id)
				}
			}
			rows.Close()
		}
		for _, statement := range []string{
			`DELETE FROM connector_oauth_state WHERE connector_id = ANY($1::uuid[])`,
			`DELETE FROM connector_oauth_client WHERE connector_id = ANY($1::uuid[])`,
			`DELETE FROM internal_connector_agent WHERE connector_id = ANY($1::uuid[])`,
			`DELETE FROM internal_connector_call_audit WHERE connector_id = ANY($1::uuid[])`,
			`DELETE FROM context_connector_credential WHERE connector_id = ANY($1::uuid[])`,
			`DELETE FROM context_capability_binding WHERE resource_id = ANY($1::uuid[])`,
			`DELETE FROM internal_connector WHERE id = ANY($1::uuid[])`,
		} {
			_, _ = testPool.Exec(bg, statement, connectorIDs)
		}
		for _, statement := range []string{
			`DELETE FROM context_config_grant WHERE agent_id = $1`,
			`DELETE FROM context_capability_binding WHERE agent_id = $1`,
			`DELETE FROM agent_task_queue WHERE agent_id = $1`,
			`DELETE FROM agent_dingtalk_identity WHERE agent_id = $1`,
			`DELETE FROM agent WHERE id = $1`,
		} {
			_, _ = testPool.Exec(bg, statement, f.agentID)
		}
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_dingtalk_identity (agent_id, workspace_id, dws_uid, org_id, bound_by, bound_at)
		VALUES ($1, $2, $3, $4, $5, now() - interval '1 hour')`, f.agentID, testWorkspaceID, "dws-"+f.agentID, catalogTestOrg, testUserID); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *catalogFixture) create(t *testing.T, app connectorcatalog.App) internalConnector {
	t.Helper()
	c, _, err := f.h.createCatalogConnector(context.Background(), testWorkspaceID, app.Slug)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *catalogFixture) grant(t *testing.T, scopeType, key string) {
	t.Helper()
	if _, err := contextcap.UpsertGrant(context.Background(), testPool, contextcap.Grant{
		UserID: testUserID, WorkspaceID: testWorkspaceID, AgentID: f.agentID, ScopeType: scopeType, OrgID: catalogTestOrg,
		ScopeKey: key, Source: contextcap.GrantSourceAgentLink,
	}, time.Hour); err != nil {
		t.Fatal(err)
	}
}

func (f *catalogFixture) offer(t *testing.T, connectorIDs ...string) {
	t.Helper()
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := contextcap.ReplaceOffers(ctx, tx, testWorkspaceID, f.agentID, connectorIDs, nil, testUserID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func (f *catalogFixture) bindPerson(t *testing.T, connectorID string) {
	t.Helper()
	if _, err := contextcap.UpsertBinding(context.Background(), testPool, contextcap.BindingWrite{
		WorkspaceID: testWorkspaceID, AgentID: f.agentID, ScopeType: contextcap.ScopePerson, OrgID: catalogTestOrg, ScopeKey: catalogTestStaff,
		ResourceType: contextcap.ResourceConnector, ResourceID: connectorID, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *catalogFixture) grantGlobally(t *testing.T, connectorID string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `INSERT INTO internal_connector_agent (connector_id, workspace_id, agent_id) VALUES ($1, $2, $3)`,
		connectorID, testWorkspaceID, f.agentID); err != nil {
		t.Fatal(err)
	}
}

func (f *catalogFixture) scope(connectorID, scopeType, key string) connectorOAuthScope {
	scope := connectorOAuthScope{WorkspaceID: testWorkspaceID, ConnectorID: connectorID, UserID: testUserID, ScopeType: scopeType}
	if scopeType != connectorOAuthScopeWorkspace {
		scope.AgentID, scope.OrgID, scope.ScopeKey = f.agentID, catalogTestOrg, key
	}
	return scope
}

// start begins a connect and hands the PKCE challenge and redirect URI to
// the fake provider, as the browser would carry them to the provider. It
// returns the authorize URL, its query and the browser binding nonce the
// start response sets as a cookie.
func (f *catalogFixture) start(t *testing.T, scope connectorOAuthScope, returnTo string) (string, url.Values, string) {
	t.Helper()
	started, err := f.h.startConnectorOAuth(context.Background(), connectorOAuthStart{connectorOAuthScope: scope, ReturnTo: returnTo})
	if err != nil {
		t.Fatalf("startConnectorOAuth: %v", err)
	}
	query := f.provideAuthorizeURL(t, started.AuthorizeURL)
	if started.Cookie == nil || started.Cookie.Value == "" || !started.Cookie.HttpOnly || started.Cookie.SameSite != http.SameSiteLaxMode ||
		started.Cookie.Name != connectorOAuthCookieName(hashConnectorOAuthState(query.Get("state"))) {
		t.Fatalf("browser binding cookie = %+v", started.Cookie)
	}
	return started.AuthorizeURL, query, started.Cookie.Value
}

// provideAuthorizeURL hands the PKCE challenge and redirect URI of a
// provider authorize URL to the fake provider and returns its query.
func (f *catalogFixture) provideAuthorizeURL(t *testing.T, authorizeURL string) url.Values {
	t.Helper()
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	f.provider.mu.Lock()
	f.provider.challenge = query.Get("code_challenge")
	f.provider.redirectURI = query.Get("redirect_uri")
	f.provider.mu.Unlock()
	return query
}

func (f *catalogFixture) task(t *testing.T, taskContext []byte) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(), `INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, context)
		VALUES ($1, $2, 'running', 0, $3) RETURNING id::text`, f.agentID, testRuntimeID, taskContext).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *catalogFixture) relay(t *testing.T, taskID, connectorID, tool string) (string, bool) {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":%q,"arguments":{}}}`, tool)
	req := httptest.NewRequest(http.MethodPost, "/api/internal-connectors/"+connectorID+"/mcp", strings.NewReader(body))
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	req.Header.Set("X-Agent-ID", f.agentID)
	req.Header.Set("X-Task-ID", taskID)
	req = withURLParam(req, "connectorId", connectorID)
	rec := httptest.NewRecorder()
	f.h.CallInternalConnector(rec, req)
	var response struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("relay %s: %d %s", tool, rec.Code, rec.Body.String())
	}
	if response.Error != nil {
		return response.Error.Message, true
	}
	if len(response.Result.Content) == 0 {
		t.Fatalf("relay %s: %d %s", tool, rec.Code, rec.Body.String())
	}
	return response.Result.Content[0].Text, response.Result.IsError
}

func (f *catalogFixture) storeTools(t *testing.T, c internalConnector) internalConnector {
	t.Helper()
	if _, err := f.h.storeCatalogConnectorTools(context.Background(), &c, []discoveredConnectorTool{{Name: "search", ReadOnly: true}, {Name: "create_issue"}}); err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *catalogFixture) sealWorkspaceOAuth(t *testing.T, connectorID string, token contextcap.OAuthToken) {
	t.Helper()
	sealed, err := f.h.sealWorkspaceConnectorSecret(testWorkspaceID, connectorID, contextcap.Secret{Bearer: token.AccessToken, OAuth: &token})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE internal_connector SET credential_ciphertext = $2 WHERE id = $1`, connectorID, sealed); err != nil {
		t.Fatal(err)
	}
}

func (f *catalogFixture) personKey(connectorID string) contextcap.CredentialBinding {
	return contextcap.CredentialBinding{WorkspaceID: testWorkspaceID, AgentID: f.agentID, ConnectorID: connectorID,
		ScopeType: contextcap.ScopePerson, OrgID: catalogTestOrg, ScopeKey: catalogTestStaff}
}

func (f *catalogFixture) sealPersonOAuth(t *testing.T, connectorID string, token contextcap.OAuthToken) {
	t.Helper()
	key := f.personKey(connectorID)
	sealed, err := contextcap.SealOAuthCredential(f.box, key, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contextcap.UpsertCredential(context.Background(), testPool, key, sealed, contextcap.OAuthHint(token.Account), testUserID); err != nil {
		t.Fatal(err)
	}
}

func (f *catalogFixture) addValidToken(token string) {
	f.provider.mu.Lock()
	f.provider.valid[token] = true
	f.provider.mu.Unlock()
}

func listedConnector(t *testing.T, h *Handler, connectorID string) (map[string]any, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ListInternalConnectors(rec, withURLParam(newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/internal-connectors", nil), "id", testWorkspaceID))
	if rec.Code != http.StatusOK {
		t.Fatalf("list connectors: %d %s", rec.Code, rec.Body.String())
	}
	var items []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item["id"] == connectorID {
			return item, rec.Body.String()
		}
	}
	t.Fatalf("connector %s not listed", connectorID)
	return nil, ""
}

// Members' available list names each connector's official app
// (catalog_slug), "" for an Aone FaaS connector.
func TestAvailableInternalConnectorsCarryCatalogSlug(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	dcr := f.storeTools(t, f.create(t, f.dcr))
	f.grantGlobally(t, dcr.ID)
	f.sealWorkspaceOAuth(t, dcr.ID, contextcap.OAuthToken{AccessToken: "acc-available", ExpiresAt: time.Now().Add(time.Hour).Unix(), Account: "octo"})
	custom := uuid.NewString()
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = testPool.Exec(bg, `DELETE FROM internal_connector_agent WHERE connector_id = $1`, custom)
		_, _ = testPool.Exec(bg, `DELETE FROM internal_connector WHERE id = $1`, custom)
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO internal_connector (id, workspace_id, name, upstream_url, credential_ref, auth_mode, allowed_tools, enabled)
		VALUES ($1, $2, $3, $4, $5, 'none', '["read"]'::jsonb, true)`,
		custom, testWorkspaceID, "Custom "+custom[:8], "https://safe.example.test/"+custom, connectorCredentialRef(custom)); err != nil {
		t.Fatal(err)
	}
	f.grantGlobally(t, custom)

	rec := httptest.NewRecorder()
	f.h.ListAvailableInternalConnectors(rec, withURLParam(newRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/internal-connectors/available", nil), "id", testWorkspaceID))
	if rec.Code != http.StatusOK {
		t.Fatalf("available connectors: %d %s", rec.Code, rec.Body.String())
	}
	var items []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	slugs := map[string]any{}
	for _, item := range items {
		if item["agent_id"] == f.agentID {
			slugs[item["id"].(string)] = item["catalog_slug"]
		}
	}
	if len(slugs) != 2 || slugs[dcr.ID] != f.dcr.Slug || slugs[custom] != "" {
		t.Fatalf("available catalog slugs = %v; body %s", slugs, rec.Body.String())
	}
}

func TestCatalogConnectorAddIsIdempotentAndBypassesOnlyTemplateURL(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	ids := make([]string, 4)
	created := make([]bool, 4)
	errs := make([]error, 4)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, isNew, err := f.h.createCatalogConnector(ctx, testWorkspaceID, f.dcr.Slug)
			ids[i], created[i], errs[i] = c.ID, isNew, err
		}(i)
	}
	wg.Wait()
	newCount := 0
	for i := range ids {
		if errs[i] != nil || ids[i] != ids[0] {
			t.Fatalf("concurrent add %d = %q %v (first %q)", i, ids[i], errs[i], ids[0])
		}
		if created[i] {
			newCount++
		}
	}
	if newCount != 1 {
		t.Fatalf("created %d connectors, want 1", newCount)
	}
	var rows int
	var authMode, upstream string
	var enabled bool
	var allowed []byte
	if err := testPool.QueryRow(ctx, `SELECT count(*) OVER (), auth_mode, upstream_url, enabled, allowed_tools FROM internal_connector
		WHERE workspace_id = $1 AND catalog_slug = $2`, testWorkspaceID, f.dcr.Slug).Scan(&rows, &authMode, &upstream, &enabled, &allowed); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || authMode != "oauth" || upstream != f.dcr.MCPURL || !enabled || string(allowed) != "[]" {
		t.Fatalf("stored catalog connector rows=%d auth=%s url=%s enabled=%v tools=%s", rows, authMode, upstream, enabled, allowed)
	}
	if _, _, err := f.h.createCatalogConnector(ctx, testWorkspaceID, "no-such-app"); !errors.Is(err, errCatalogAppUnknown) {
		t.Fatalf("unknown app: %v", err)
	}

	// Host bypass: exactly the template URL, never a variant or a custom
	// connector supplying it.
	if err := validateConnectorURL(f.dcr.MCPURL); err != nil {
		t.Fatalf("template URL rejected: %v", err)
	}
	for _, raw := range []string{f.dcr.MCPURL + "/other", strings.Replace(f.dcr.MCPURL, "/mcp", "/mcp2", 1), f.provider.URL + "/"} {
		if validateConnectorURL(raw) == nil {
			t.Fatalf("non-template URL %q bypassed the host allowlist", raw)
		}
	}
	if _, err := f.h.prepareInternalConnectorCreate(ctx, &connectorInput{Name: "Copy", UpstreamURL: f.dcr.MCPURL, AuthMode: "bearer", AllowedTools: []string{"search"}}, uuid.NewString(), testWorkspaceID); err == nil {
		t.Fatal("custom connector accepted an official app URL")
	}
	if err := validateConnectorInput(connectorInput{Name: "Demo", UpstreamURL: f.dcr.MCPURL, AuthMode: "oauth", catalogSlug: f.dcr.Slug}); err != nil {
		t.Fatalf("catalog connector without tools rejected: %v", err)
	}
	if validateConnectorInput(connectorInput{Name: "Demo", UpstreamURL: f.dcr.MCPURL, AuthMode: "oauth", AllowedTools: []string{"search"}}) == nil {
		t.Fatal("auth_mode oauth accepted for a custom connector")
	}
	if validateConnectorInput(connectorInput{Name: "Demo", UpstreamURL: f.gh.MCPURL, AuthMode: "oauth", catalogSlug: f.dcr.Slug}) == nil {
		t.Fatal("catalog connector accepted another app's URL")
	}

	apps, err := f.h.connectorCatalogApps(ctx, testWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, app := range apps {
		switch app.Slug {
		case f.dcr.Slug:
			if app.ConnectorID == nil || *app.ConnectorID != ids[0] || !app.OAuthAvailable || app.AllowsPAT {
				t.Fatalf("dcr catalog view = %+v", app)
			}
		case f.gh.Slug:
			if app.ConnectorID != nil || !app.AllowsPAT || !app.OAuthAvailable || app.AuthKind != "oauth_github_app" {
				t.Fatalf("github catalog view = %+v", app)
			}
		}
	}
	item, _ := listedConnector(t, f.h, ids[0])
	if item["catalog_slug"] != f.dcr.Slug || item["write_enabled"] != false || item["discovered_tool_count"] != float64(0) ||
		item["credential_account"] != "" || item["credential_optional"] != true || item["auth_mode"] != "oauth" {
		t.Fatalf("listed catalog connector = %v", item)
	}

	// A globally granted catalog connector with a credential but no tools
	// yet is not mounted.
	f.grantGlobally(t, ids[0])
	f.addValidToken("pat-demo-token")
	sealed, err := f.h.sealWorkspaceConnectorSecret(testWorkspaceID, ids[0], contextcap.Secret{Bearer: "pat-demo-token"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE internal_connector SET credential_ciphertext = $2 WHERE id = $1`, ids[0], sealed); err != nil {
		t.Fatal(err)
	}
	task := f.task(t, nil)
	connectors, err := f.h.authorizedTaskConnectors(ctx, parseUUID(testWorkspaceID), db.AgentTaskQueue{ID: parseUUID(task), AgentID: parseUUID(f.agentID)})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range connectors {
		if c.ID == ids[0] {
			t.Fatal("catalog connector without discovered tools was mounted")
		}
	}
}

func TestCatalogConnectorDCRConnectPersonScopePinsToolsThroughSession(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	c := f.create(t, f.dcr)
	f.offer(t, c.ID)
	f.grant(t, contextcap.ScopePerson, catalogTestStaff)
	scope := f.scope(c.ID, contextcap.ScopePerson, catalogTestStaff)

	authorizeURL, query, firstNonce := f.start(t, scope, "/dingtalk/configure?agent="+f.agentID)
	if !strings.HasPrefix(authorizeURL, f.provider.URL+"/authorize?") || query.Get("client_id") != "dcr-client-1" ||
		query.Get("redirect_uri") != catalogAppOrigin+connectorOAuthCallbackPath || query.Get("code_challenge_method") != "S256" ||
		query.Get("resource") != f.dcr.MCPURL || !strings.HasPrefix(query.Get("state"), connectorOAuthStatePrefix) {
		t.Fatalf("authorize URL = %s", authorizeURL)
	}
	state := query.Get("state")
	var stored int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM connector_oauth_state WHERE state_hash = $1 AND connector_id = $2`, hashConnectorOAuthState(state), c.ID).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("state stored hashed: %d %v", stored, err)
	}
	var plainState int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM connector_oauth_state WHERE state_hash = $1`, state).Scan(&plainState); err != nil || plainState != 0 {
		t.Fatalf("plain state stored: %d %v", plainState, err)
	}
	// A second connect reuses the dynamic client registration.
	_, second, secondNonce := f.start(t, scope, "")
	if registrations, _, _, _ := f.provider.counts(); registrations != 1 {
		t.Fatalf("registrations = %d, want 1", registrations)
	}
	// The latest start's challenge is what the provider holds; complete the
	// second flow, then show the first state cannot be used with it.
	outcome := f.h.completeConnectorOAuth(ctx, connectorOAuthCallback{Via: connectorOAuthViaDCR, State: second.Get("state"), Code: "good-code", BrowserNonce: secondNonce})
	if outcome.ErrorCode != "" || outcome.Slug != f.dcr.Slug || outcome.Account != "octo" || outcome.Discovered != 2 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if want := catalogAppOrigin + "/dingtalk/configure?agent=" + url.QueryEscape(f.agentID) + "&connected=" + f.dcr.Slug; outcome.RedirectURL != want {
		t.Fatalf("redirect = %q, want %q", outcome.RedirectURL, want)
	}
	if _, _, _, initializes := f.provider.counts(); initializes == 0 {
		t.Fatal("tool discovery did not establish the MCP session the server requires")
	}
	credential, err := contextcap.GetCredential(ctx, testPool, f.personKey(c.ID))
	if err != nil {
		t.Fatal(err)
	}
	if credential.Hint != "@octo" || contextcap.CredentialKindFromHint(credential.Hint) != contextcap.CredentialKindOAuth || bytes.Contains(credential.Ciphertext, []byte("acc-")) {
		t.Fatalf("stored credential hint=%q", credential.Hint)
	}
	secret, err := contextcap.OpenCredentialSecret(f.box, f.personKey(c.ID), credential.Ciphertext)
	if err != nil || secret.OAuth == nil || secret.OAuth.RefreshToken == "" || secret.OAuth.Account != "octo" ||
		secret.OAuth.ExpiresAt < time.Now().Add(50*time.Minute).Unix() || secret.OAuth.ClientID != "dcr-client-1" {
		t.Fatalf("stored secret = %+v %v", secret, err)
	}
	loaded, err := f.h.loadInternalConnector(ctx, testWorkspaceID, c.ID)
	if err != nil || len(loaded.DiscoveredTools) != 2 || len(loaded.AllowedTools) != 1 || loaded.AllowedTools[0] != "search" {
		t.Fatalf("pinned tools = %+v %v", loaded.AllowedTools, err)
	}
	// The old state of the first start was replaced by nothing: it is still
	// single use and valid, but its PKCE challenge no longer matches.
	first := f.h.completeConnectorOAuth(ctx, connectorOAuthCallback{Via: connectorOAuthViaDCR, State: state, Code: "good-code", BrowserNonce: firstNonce})
	if first.ErrorCode != connectOAuthErrExchangeFailed {
		t.Fatalf("first state with a foreign verifier = %+v", first)
	}
	// Replay of a consumed state has no trusted destination.
	replay := f.h.completeConnectorOAuth(ctx, connectorOAuthCallback{Via: connectorOAuthViaDCR, State: second.Get("state"), Code: "good-code", BrowserNonce: secondNonce})
	if replay.ErrorCode != connectOAuthErrInvalidState || replay.RedirectURL != "" {
		t.Fatalf("replay = %+v", replay)
	}

	// Connecting turned the offered connector on for the person.
	bindings, err := contextcap.ListScopeBindings(ctx, testPool, testWorkspaceID, f.agentID, contextcap.ScopePerson, catalogTestOrg, catalogTestStaff)
	if err != nil || len(bindings) != 1 || bindings[0].ResourceID != c.ID || !bindings[0].Enabled {
		t.Fatalf("person bindings after connect = %+v %v", bindings, err)
	}
	// The person's run uses the person's account through the relay.
	task := f.task(t, ctxcapDispatch("single", "", catalogTestStaff))
	if text, isError := f.relay(t, task, c.ID, "search"); isError || text != "ok:search" {
		t.Fatalf("relay = %q %v", text, isError)
	}
	if text, isError := f.relay(t, task, c.ID, "create_issue"); !isError || strings.Contains(text, "ok:") {
		t.Fatalf("write tool relayed while writes are off: %q %v", text, isError)
	}
	var layer string
	if err := testPool.QueryRow(ctx, `SELECT credential_layer FROM internal_connector_call_audit WHERE connector_id = $1 AND task_id = $2 AND outcome = 'ok'`, c.ID, task).Scan(&layer); err != nil || layer != contextcap.ScopePerson {
		t.Fatalf("audit credential layer = %q %v", layer, err)
	}
}

func TestCatalogConnectorOAuthStartAndCallbackAuthorization(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	c := f.create(t, f.dcr)
	person := f.scope(c.ID, contextcap.ScopePerson, catalogTestStaff)
	scene := f.scope(c.ID, contextcap.ScopeScene, catalogTestScene)
	start := func(scope connectorOAuthScope, returnTo string) error {
		_, err := f.h.startConnectorOAuth(ctx, connectorOAuthStart{connectorOAuthScope: scope, ReturnTo: returnTo})
		return err
	}
	status := func(err error) int {
		var oauthErr *connectorOAuthError
		if errors.As(err, &oauthErr) {
			return oauthErr.Status
		}
		return 0
	}
	if err := start(person, ""); status(err) != http.StatusForbidden {
		t.Fatalf("person without grant: %v", err)
	}
	f.grant(t, contextcap.ScopePerson, catalogTestStaff)
	f.grant(t, contextcap.ScopeScene, catalogTestScene)
	if err := start(person, ""); status(err) != http.StatusForbidden {
		t.Fatalf("person grant, connector neither offered nor granted: %v", err)
	}
	f.grantGlobally(t, c.ID)
	if err := start(person, ""); err != nil {
		t.Fatalf("person with a globally granted connector: %v", err)
	}
	if err := start(scene, ""); status(err) != http.StatusForbidden {
		t.Fatalf("scene connect needs an offered connector: %v", err)
	}
	f.offer(t, c.ID)
	if err := start(scene, ""); err != nil {
		t.Fatalf("scene with offer: %v", err)
	}
	outsider := f.scope(c.ID, connectorOAuthScopeWorkspace, "")
	outsider.UserID = uuid.NewString()
	if err := start(outsider, ""); status(err) != http.StatusForbidden {
		t.Fatalf("workspace scope for a non-member: %v", err)
	}
	if err := start(f.scope(c.ID, connectorOAuthScopeWorkspace, ""), ""); err != nil {
		t.Fatalf("workspace scope for the owner: %v", err)
	}
	for _, returnTo := range []string{"https://evil.example/x", "//evil.example/x", "javascript:alert(1)", "https://app.example.test.evil.example/"} {
		if err := start(person, returnTo); status(err) != http.StatusBadRequest {
			t.Fatalf("return_to %q: %v", returnTo, err)
		}
	}
	if err := start(person, catalogWebOrigin+"/dingtalk/configure?agent=x#frag"); err != nil {
		t.Fatalf("return_to on the frontend origin: %v", err)
	}
	// Expired state.
	_, query, nonce := f.start(t, person, "")
	if _, err := testPool.Exec(ctx, `UPDATE connector_oauth_state SET expires_at = now() - interval '1 second' WHERE state_hash = $1`, hashConnectorOAuthState(query.Get("state"))); err != nil {
		t.Fatal(err)
	}
	if outcome := f.h.completeConnectorOAuth(ctx, connectorOAuthCallback{Via: connectorOAuthViaDCR, State: query.Get("state"), Code: "good-code", BrowserNonce: nonce}); outcome.ErrorCode != connectOAuthErrInvalidState || outcome.RedirectURL != "" {
		t.Fatalf("expired state = %+v", outcome)
	}
	// A DCR state delivered to the GitHub callback is refused (and consumed).
	_, query, nonce = f.start(t, person, "")
	if outcome := f.h.completeConnectorOAuth(ctx, connectorOAuthCallback{Via: connectorOAuthViaGitHub, State: query.Get("state"), Code: "good-code", BrowserNonce: nonce}); outcome.ErrorCode != connectOAuthErrInvalidState {
		t.Fatalf("via mismatch = %+v", outcome)
	}
	if outcome := f.h.completeConnectorOAuth(ctx, connectorOAuthCallback{Via: connectorOAuthViaDCR, State: query.Get("state"), Code: "good-code", BrowserNonce: nonce}); outcome.ErrorCode != connectOAuthErrInvalidState {
		t.Fatalf("consumed state reused = %+v", outcome)
	}
	// The provider reports a denial.
	_, query, nonce = f.start(t, person, "/dingtalk/configure?agent="+f.agentID+"&connected=stale")
	outcome := f.h.completeConnectorOAuth(ctx, connectorOAuthCallback{Via: connectorOAuthViaDCR, State: query.Get("state"), Error: "access_denied", BrowserNonce: nonce})
	if outcome.ErrorCode != connectOAuthErrAccessDenied || !strings.Contains(outcome.RedirectURL, "connect_error=access_denied") || strings.Contains(outcome.RedirectURL, "connected=") ||
		!strings.HasPrefix(outcome.RedirectURL, catalogAppOrigin+"/dingtalk/configure?") {
		t.Fatalf("denied = %+v", outcome)
	}
	// Any other provider error is not reported as a cancellation.
	_, query, nonce = f.start(t, person, "")
	outcome = f.h.completeConnectorOAuth(ctx, connectorOAuthCallback{Via: connectorOAuthViaDCR, State: query.Get("state"), Error: "invalid_scope", BrowserNonce: nonce})
	if outcome.ErrorCode != connectOAuthErrProviderError || !strings.HasSuffix(outcome.RedirectURL, "connect_error="+connectOAuthErrProviderError) {
		t.Fatalf("provider error = %+v", outcome)
	}
	// Browser binding: a state completes only in the browser that started it.
	// Another browser (no cookie, or another state's cookie) is refused and
	// burns the state; the code is never exchanged.
	_, _, otherNonce := f.start(t, person, "")
	for _, presented := range []string{"", otherNonce, otherNonce + "x"} {
		_, query, nonce = f.start(t, person, "")
		outcome = f.h.completeConnectorOAuth(ctx, connectorOAuthCallback{Via: connectorOAuthViaDCR, State: query.Get("state"), Code: "good-code", BrowserNonce: presented})
		if outcome.ErrorCode != connectOAuthErrBrowserMismatch || !strings.HasSuffix(outcome.RedirectURL, "connect_error="+connectOAuthErrBrowserMismatch) {
			t.Fatalf("callback with nonce %q = %+v", presented, outcome)
		}
		if replay := f.h.completeConnectorOAuth(ctx, connectorOAuthCallback{Via: connectorOAuthViaDCR, State: query.Get("state"), Code: "good-code", BrowserNonce: nonce}); replay.ErrorCode != connectOAuthErrInvalidState {
			t.Fatalf("state after a browser mismatch = %+v", replay)
		}
	}
	if _, err := contextcap.GetCredential(ctx, testPool, f.personKey(c.ID)); !errors.Is(err, contextcap.ErrNotFound) {
		t.Fatalf("credential stored from another browser: %v", err)
	}
	// The grant disappears between start and callback.
	_, query, nonce = f.start(t, person, "")
	if _, err := testPool.Exec(ctx, `DELETE FROM context_config_grant WHERE agent_id = $1 AND scope_type = 'person'`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if outcome := f.h.completeConnectorOAuth(ctx, connectorOAuthCallback{Via: connectorOAuthViaDCR, State: query.Get("state"), Code: "good-code", BrowserNonce: nonce}); outcome.ErrorCode != connectOAuthErrForbidden {
		t.Fatalf("revoked grant = %+v", outcome)
	}
	if _, exchanges, _, _ := f.provider.counts(); exchanges != 0 {
		t.Fatalf("code exchanged %d times for refused callbacks", exchanges)
	}
}

func TestCatalogConnectorGitHubAppFlowUsesSharedCallbackAndHidesTokens(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	c := f.create(t, f.gh)
	scope := f.scope(c.ID, connectorOAuthScopeWorkspace, "")

	authorizeURL, query, nonce := f.start(t, scope, "")
	if !strings.HasPrefix(authorizeURL, f.gh.AuthorizationEndpoint+"?") || query.Get("client_id") != "gh-client" ||
		query.Get("redirect_uri") != catalogWebOrigin+connectorOAuthGitHubCallback || query.Get("code_challenge_method") != "S256" ||
		query.Get("resource") != "" || !isConnectorOAuthState(query.Get("state")) {
		t.Fatalf("GitHub authorize URL = %s", authorizeURL)
	}
	if registrations, _, _, _ := f.provider.counts(); registrations != 0 {
		t.Fatal("GitHub App flow used dynamic registration")
	}
	if outcome := f.h.completeConnectorOAuth(ctx, connectorOAuthCallback{Via: connectorOAuthViaDCR, State: query.Get("state"), Code: "good-code", BrowserNonce: nonce}); outcome.ErrorCode != connectOAuthErrInvalidState {
		t.Fatalf("GitHub state on the DCR callback = %+v", outcome)
	}
	_, query, nonce = f.start(t, scope, "")
	outcome := f.h.completeConnectorOAuth(ctx, connectorOAuthCallback{Via: connectorOAuthViaGitHub, State: query.Get("state"), Code: "good-code", BrowserNonce: nonce})
	if outcome.ErrorCode != "" || outcome.Account != "octo" || outcome.Discovered != 2 {
		t.Fatalf("GitHub outcome = %+v", outcome)
	}
	if !strings.HasPrefix(outcome.RedirectURL, catalogAppOrigin+"/") || !strings.HasSuffix(outcome.RedirectURL, "/internal-connectors?connected="+f.gh.Slug) {
		t.Fatalf("GitHub redirect = %q", outcome.RedirectURL)
	}
	f.provider.mu.Lock()
	secretSent, resourceSent := f.provider.lastClientSecret, f.provider.lastResourceParam
	f.provider.mu.Unlock()
	if secretSent != "gh-secret" || resourceSent != "" {
		t.Fatalf("GitHub code exchange client_secret=%q resource=%q", secretSent, resourceSent)
	}
	item, body := listedConnector(t, f.h, c.ID)
	if item["credential_account"] != "@octo" || item["credential_source"] != "workspace" || item["credential_ready"] != true ||
		item["discovered_tool_count"] != float64(2) {
		t.Fatalf("listed GitHub connector = %v", item)
	}
	assertNoTokenMaterial(t, "connector list", body)
	if !connectorAcceptsBearer("oauth", f.gh.Slug) || connectorAcceptsBearer("oauth", f.dcr.Slug) || !connectorAcceptsBearer("bearer", "") || connectorAcceptsBearer("none", "") {
		t.Fatal("PAT acceptance rules")
	}
}

func TestCatalogConnectorConcurrentRefreshRequestsOneToken(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	c := f.create(t, f.gh)
	f.sealPersonOAuth(t, c.ID, contextcap.OAuthToken{AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(10 * time.Second).Unix(), Account: "octo"})
	f.provider.mu.Lock()
	f.provider.refreshDelay = 300 * time.Millisecond
	f.provider.mu.Unlock()

	credential, err := contextcap.GetCredential(ctx, testPool, f.personKey(c.ID))
	if err != nil {
		t.Fatal(err)
	}
	secret, err := contextcap.OpenCredentialSecret(f.box, f.personKey(c.ID), credential.Ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	tokens := make([]string, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range tokens {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resolved := c
			resolved.setResolvedSecret(secret, contextcap.ScopePerson, f.personKey(c.ID))
			tokens[i], errs[i] = f.h.freshConnectorToken(ctx, &resolved, "")
		}(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || tokens[0] != tokens[1] || tokens[0] == "old-access" {
		t.Fatalf("concurrent refresh = %v %v %v", tokens, errs[0], errs[1])
	}
	if _, _, refreshes, _ := f.provider.counts(); refreshes != 1 {
		t.Fatalf("token endpoint refreshed %d times, want 1", refreshes)
	}
	credential, err = contextcap.GetCredential(ctx, testPool, f.personKey(c.ID))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := contextcap.OpenCredentialSecret(f.box, f.personKey(c.ID), credential.Ciphertext)
	if err != nil || stored.Bearer != tokens[0] || stored.OAuth.RefreshToken == "old-refresh" || stored.OAuth.Account != "octo" || credential.Hint != "@octo" {
		t.Fatalf("stored after refresh = %+v %v (hint %q)", stored, err, credential.Hint)
	}
}

func TestCatalogConnectorUpstream401RefreshesAndRetriesOnce(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	c := f.storeTools(t, f.create(t, f.gh))
	f.grantGlobally(t, c.ID)
	// Not near expiry, but the provider no longer accepts it.
	f.sealWorkspaceOAuth(t, c.ID, contextcap.OAuthToken{AccessToken: "revoked-access", RefreshToken: "live-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	task := f.task(t, nil)
	if text, isError := f.relay(t, task, c.ID, "search"); isError || text != "ok:search" {
		t.Fatalf("relay after 401 = %q %v", text, isError)
	}
	if _, _, refreshes, _ := f.provider.counts(); refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes)
	}
	var ciphertext []byte
	if err := testPool.QueryRow(ctx, `SELECT credential_ciphertext FROM internal_connector WHERE id = $1`, c.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	stored, err := f.h.openWorkspaceConnectorSecret(testWorkspaceID, c.ID, ciphertext)
	if err != nil || stored.Bearer == "revoked-access" || stored.OAuth == nil || stored.OAuth.RefreshToken == "live-refresh" {
		t.Fatalf("workspace credential after forced refresh = %+v %v", stored, err)
	}
	f.provider.mu.Lock()
	calls := append([]string(nil), f.provider.toolCalls...)
	f.provider.mu.Unlock()
	if len(calls) != 1 || calls[0] != "search" {
		t.Fatalf("upstream tool calls = %v", calls)
	}
	// A rejected PAT (no OAuth part) is not retried.
	sealed, err := f.h.sealWorkspaceConnectorSecret(testWorkspaceID, c.ID, contextcap.Secret{Bearer: "rejected-pat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE internal_connector SET credential_ciphertext = $2 WHERE id = $1`, c.ID, sealed); err != nil {
		t.Fatal(err)
	}
	if text, isError := f.relay(t, task, c.ID, "search"); !isError || text != "Internal MCP upstream unavailable" {
		t.Fatalf("rejected PAT relay = %q %v", text, isError)
	}
	if _, _, refreshes, _ := f.provider.counts(); refreshes != 1 {
		t.Fatalf("PAT triggered a refresh: %d", refreshes)
	}
}

func TestCatalogConnectorInvalidGrantDeletesCredential(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	c := f.storeTools(t, f.create(t, f.gh))
	f.offer(t, c.ID)
	f.bindPerson(t, c.ID)
	f.provider.mu.Lock()
	f.provider.refreshMode = "invalid_grant"
	f.provider.mu.Unlock()
	// Expired but refreshable: still mounted; the refresh is rejected.
	f.sealPersonOAuth(t, c.ID, contextcap.OAuthToken{AccessToken: "expired-access", RefreshToken: "revoked-refresh", ExpiresAt: time.Now().Add(-time.Minute).Unix()})
	task := f.task(t, ctxcapDispatch("single", "", catalogTestStaff))
	text, isError := f.relay(t, task, c.ID, "search")
	if !isError || !strings.Contains(text, "reconnect") || strings.Contains(text, "revoked-refresh") {
		t.Fatalf("invalid_grant relay = %q %v", text, isError)
	}
	if _, err := contextcap.GetCredential(ctx, testPool, f.personKey(c.ID)); !errors.Is(err, contextcap.ErrNotFound) {
		t.Fatalf("person credential after invalid_grant: %v", err)
	}
	var outcome string
	if err := testPool.QueryRow(ctx, `SELECT outcome FROM internal_connector_call_audit WHERE connector_id = $1 AND task_id = $2 AND outcome <> 'forwarded'`, c.ID, task).Scan(&outcome); err != nil || outcome != "reconnect_required" {
		t.Fatalf("audit outcome = %q %v", outcome, err)
	}

	// Workspace credential: the ciphertext is cleared.
	f.sealWorkspaceOAuth(t, c.ID, contextcap.OAuthToken{AccessToken: "ws-expired", RefreshToken: "ws-revoked", ExpiresAt: time.Now().Add(-time.Minute).Unix()})
	loaded, err := f.h.loadInternalConnector(ctx, testWorkspaceID, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.freshConnectorToken(ctx, &loaded, ""); !errors.Is(err, errConnectorReconnectRequired) {
		t.Fatalf("workspace refresh = %v", err)
	}
	var ciphertext []byte
	if err := testPool.QueryRow(ctx, `SELECT credential_ciphertext FROM internal_connector WHERE id = $1`, c.ID).Scan(&ciphertext); err != nil || ciphertext != nil {
		t.Fatalf("workspace ciphertext after invalid_grant = %v %v", ciphertext, err)
	}
}

func TestCatalogConnectorWriteEnabledRecomputesPinnedTools(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	c := f.storeTools(t, f.create(t, f.dcr))
	if len(c.AllowedTools) != 1 || c.AllowedTools[0] != "search" {
		t.Fatalf("read-only pin = %v", c.AllowedTools)
	}
	allowed, err := f.h.setCatalogConnectorWriteEnabled(ctx, testWorkspaceID, c.ID, true)
	if err != nil || strings.Join(allowed, ",") != "search,create_issue" {
		t.Fatalf("write on = %v %v", allowed, err)
	}
	allowed, err = f.h.setCatalogConnectorWriteEnabled(ctx, testWorkspaceID, c.ID, false)
	if err != nil || strings.Join(allowed, ",") != "search" {
		t.Fatalf("write off = %v %v", allowed, err)
	}

	// The PATCH update accepts write_enabled for catalog connectors and
	// ignores client-supplied tools, which are server-managed.
	patch := func(connectorID string, body map[string]any) *httptest.ResponseRecorder {
		req := withURLParams(newRequest(http.MethodPatch, "/api/workspaces/"+testWorkspaceID+"/internal-connectors/"+connectorID, body), "id", testWorkspaceID, "connectorId", connectorID)
		rec := httptest.NewRecorder()
		f.h.UpdateInternalConnector(rec, req)
		return rec
	}
	rec := patch(c.ID, map[string]any{"name": "Demo team", "upstream_url": f.dcr.MCPURL, "auth_mode": "oauth", "allowed_tools": []string{"anything"},
		"agent_ids": []string{}, "enabled": true, "write_enabled": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog PATCH: %d %s", rec.Code, rec.Body.String())
	}
	loaded, err := f.h.loadInternalConnector(ctx, testWorkspaceID, c.ID)
	if err != nil || !loaded.WriteEnabled || strings.Join(loaded.AllowedTools, ",") != "search,create_issue" || loaded.Name != "Demo team" {
		t.Fatalf("after PATCH = %+v %v", loaded, err)
	}
	custom := uuid.NewString()
	if _, err := testPool.Exec(ctx, `INSERT INTO internal_connector (id, workspace_id, name, upstream_url, credential_ref, auth_mode, allowed_tools, enabled)
		VALUES ($1, $2, 'Custom', 'https://safe.example.test/mcp', $3, 'none', '["read"]'::jsonb, false)`, custom, testWorkspaceID, connectorCredentialRef(custom)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM internal_connector WHERE id = $1`, custom)
	})
	rec = patch(custom, map[string]any{"name": "Custom", "upstream_url": "https://safe.example.test/mcp", "allowed_tools": []string{"read"},
		"agent_ids": []string{}, "enabled": false, "write_enabled": true})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("custom PATCH with write_enabled: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := f.h.setCatalogConnectorWriteEnabled(ctx, testWorkspaceID, custom, true); !errors.Is(err, errConnectorNotCatalog) {
		t.Fatalf("custom write toggle: %v", err)
	}
}

func TestCatalogConnectorRefreshToolsUsesAnyConnectedAccount(t *testing.T) {
	f := newCatalogFixture(t)
	ctx := context.Background()
	c := f.create(t, f.dcr)
	if _, err := f.h.refreshCatalogConnectorTools(ctx, testWorkspaceID, c.ID); !errors.Is(err, errCatalogNoCredential) {
		t.Fatalf("refresh without an account: %v", err)
	}
	f.addValidToken("person-access")
	f.sealPersonOAuth(t, c.ID, contextcap.OAuthToken{AccessToken: "person-access", Account: "octo"})
	result, err := f.h.refreshCatalogConnectorTools(ctx, testWorkspaceID, c.ID)
	if err != nil || result.Discovered != 2 || strings.Join(result.AllowedTools, ",") != "search" {
		t.Fatalf("refresh tools = %+v %v", result, err)
	}
}

func TestCatalogConnectorWorkspaceDeleteSweepsOAuthTables(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	var wsID string
	if err := testPool.QueryRow(ctx, `INSERT INTO workspace (name, slug, description) VALUES ('Catalog delete', $1, '') RETURNING id::text`,
		"catalog-delete-"+uuid.NewString()[:8]).Scan(&wsID); err != nil {
		t.Fatal(err)
	}
	deleted, kept := uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = testPool.Exec(bg, `DELETE FROM workspace WHERE id = $1`, wsID)
		for _, table := range []string{"connector_oauth_state", "connector_oauth_client"} {
			_, _ = testPool.Exec(bg, `DELETE FROM `+table+` WHERE connector_id = ANY($1::uuid[])`, []string{deleted, kept})
		}
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'owner')`, wsID, testUserID); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ workspace, connector string }{{wsID, deleted}, {testWorkspaceID, kept}} {
		if _, err := testPool.Exec(ctx, `INSERT INTO connector_oauth_client (connector_id, workspace_id, authorization_endpoint, token_endpoint, redirect_uri, registration_ciphertext)
			VALUES ($1, $2, 'https://a.example/authorize', 'https://a.example/token', 'https://app.example/cb', '\x00'::bytea)`, row.connector, row.workspace); err != nil {
			t.Fatal(err)
		}
		if _, err := testPool.Exec(ctx, `INSERT INTO connector_oauth_state (state_hash, workspace_id, connector_id, scope_type, user_id, verifier_ciphertext, expires_at)
			VALUES (md5(random()::text) || md5(random()::text), $1, $2, 'workspace', $3, '\x00'::bytea, now() + interval '10 minutes')`, row.workspace, row.connector, testUserID); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	testHandler.DeleteWorkspace(rec, withURLParam(newRequest(http.MethodDelete, "/api/workspaces/"+wsID, nil), "id", wsID))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete workspace: %d %s", rec.Code, rec.Body.String())
	}
	for _, table := range []string{"connector_oauth_state", "connector_oauth_client"} {
		var gone, stays int
		if err := testPool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE connector_id = $1), count(*) FILTER (WHERE connector_id = $2) FROM `+table,
			deleted, kept).Scan(&gone, &stays); err != nil {
			t.Fatal(err)
		}
		if gone != 0 || stays != 1 {
			t.Errorf("%s: deleted-workspace rows=%d other-workspace rows=%d", table, gone, stays)
		}
	}
}

func TestPinnedCatalogToolsOrderingAndLimits(t *testing.T) {
	discovered := []discoveredConnectorTool{
		{Name: "create"}, {Name: "read_a", ReadOnly: true}, {Name: "bad,name", ReadOnly: true}, {Name: "read_b", ReadOnly: true},
		{Name: strings.Repeat("x", 129), ReadOnly: true}, {Name: "read_a", ReadOnly: true},
	}
	if got := strings.Join(pinnedCatalogTools(discovered, false), ","); got != "read_a,read_b" {
		t.Fatalf("read-only pin = %q", got)
	}
	if got := strings.Join(pinnedCatalogTools(discovered, true), ","); got != "read_a,read_b,create" {
		t.Fatalf("write pin = %q", got)
	}
	many := make([]discoveredConnectorTool, 0, 100)
	for i := 0; i < 100; i++ {
		many = append(many, discoveredConnectorTool{Name: fmt.Sprintf("tool_%03d", i), ReadOnly: i%2 == 0})
	}
	pinned := pinnedCatalogTools(many, true)
	if len(pinned) != maxPinnedConnectorTools || pinned[0] != "tool_000" || pinned[49] != "tool_098" || pinned[50] != "tool_001" {
		t.Fatalf("capped pin = %d %v", len(pinned), pinned[:3])
	}
}

func TestConnectorOAuthStateAndResultHelpers(t *testing.T) {
	state, err := randomOAuthValue()
	if err != nil {
		t.Fatal(err)
	}
	if !validConnectorOAuthState(connectorOAuthStatePrefix+state) || validConnectorOAuthState(state) || validConnectorOAuthState(connectorOAuthStatePrefix+"short") {
		t.Fatal("state format check")
	}
	if !isConnectorOAuthState(connectorOAuthStatePrefix+state) || isConnectorOAuthState("eyJhbGciOiJIUzI1NiJ9.x.y") {
		t.Fatal("GitHub state dispatch prefix")
	}
	got := withOAuthResult("https://app.example.test/dingtalk/configure?agent=a&connect_error=old", "connected", "github")
	if got != "https://app.example.test/dingtalk/configure?agent=a&connected=github" {
		t.Fatalf("withOAuthResult = %q", got)
	}
}
