package remotemcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// sessionServer is a fake Streamable HTTP MCP server. requireSession makes it
// answer 400 to any non-initialize request without a valid session, the way
// Cloudflare-hosted official servers do.
type sessionServer struct {
	*httptest.Server
	requireSession bool
	sse            bool

	mu          sync.Mutex
	sessions    map[string]bool
	initialized atomic.Int32
	calls       atomic.Int32
	lastVersion string
	status      int // forced HTTP status for tools/* when non-zero
	// unknownSessionStatus answers a request with an unknown session id
	// (default 404; the MCP SDK's session-map servers answer 400).
	unknownSessionStatus int
	// rpcError, when set, answers tools/* with this JSON-RPC error message
	// after counting the call as executed.
	rpcError string
	// sessionIDs records the JSON-RPC ids seen per session id.
	sessionIDs map[string][]string
}

func newSessionServer(t *testing.T, requireSession, sse bool) *sessionServer {
	t.Helper()
	s := &sessionServer{requireSession: requireSession, sse: sse, sessions: map[string]bool{}}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *sessionServer) expireSessions() {
	s.mu.Lock()
	s.sessions = map[string]bool{}
	s.mu.Unlock()
}

func (s *sessionServer) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer good" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	switch req.Method {
	case "initialize":
		s.initialized.Add(1)
		id := fmt.Sprintf("session-%d", s.initialized.Load())
		s.mu.Lock()
		s.sessions[id] = true
		s.mu.Unlock()
		w.Header().Set("Mcp-Session-Id", id)
		s.write(w, req.ID, map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}})
		return
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
		return
	}
	s.mu.Lock()
	sessionID := r.Header.Get("Mcp-Session-Id")
	known := s.sessions[sessionID]
	s.lastVersion = r.Header.Get("MCP-Protocol-Version")
	forced, rpcError, unknownStatus := s.status, s.rpcError, s.unknownSessionStatus
	if known {
		if s.sessionIDs == nil {
			s.sessionIDs = map[string][]string{}
		}
		s.sessionIDs[sessionID] = append(s.sessionIDs[sessionID], string(req.ID))
	}
	s.mu.Unlock()
	if sessionID != "" && !known {
		if unknownStatus == 0 {
			unknownStatus = http.StatusNotFound
		}
		w.WriteHeader(unknownStatus)
		return
	}
	if s.requireSession && sessionID == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.calls.Add(1)
	if forced != 0 {
		w.WriteHeader(forced)
		return
	}
	if rpcError != "" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32603, "message": rpcError}})
		return
	}
	switch req.Method {
	case "tools/list":
		var params struct {
			Cursor string `json:"cursor"`
		}
		_ = json.Unmarshal(req.Params, &params)
		if params.Cursor == "" {
			s.write(w, req.ID, map[string]any{"tools": []any{
				map[string]any{"name": "search", "annotations": map[string]any{"readOnlyHint": true}},
				map[string]any{"name": "create_issue"},
			}, "nextCursor": "page-2"})
			return
		}
		s.write(w, req.ID, map[string]any{"tools": []any{
			map[string]any{"name": "get_me", "annotations": map[string]any{"readOnlyHint": true}},
		}})
	case "tools/call":
		s.write(w, req.ID, map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}})
	default:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
	}
}

func (s *sessionServer) write(w http.ResponseWriter, id json.RawMessage, result any) {
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if s.sse {
		w.Header().Set("Content-Type", "text/event-stream")
		// A notification first, then the response, then the stream stays open.
		fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
}

func (s *sessionServer) client(t *testing.T) *ExternalClient {
	t.Helper()
	u, _ := url.Parse(s.URL)
	return NewExternalClientWithTransport([]string{u.Host}, s.Client().Transport)
}

func bearer(token string) http.Header {
	return http.Header{"Authorization": []string{"Bearer " + token}}
}

func TestSessionClientEstablishesSessionOnlyWhenRequired(t *testing.T) {
	for _, sse := range []bool{false, true} {
		server := newSessionServer(t, true, sse)
		cache := NewSessionCache(10*time.Minute, 16)
		mcp, err := server.client(t).MCP(server.URL+"/mcp", cache)
		if err != nil {
			t.Fatal(err)
		}
		tools, truncated, err := mcp.ListTools(context.Background(), "k1", bearer("good"), 256)
		if err != nil || truncated || len(tools) != 3 || !tools[0].ReadOnly || tools[1].ReadOnly || tools[2].Name != "get_me" {
			t.Fatalf("sse=%v ListTools = %+v %v %v", sse, tools, truncated, err)
		}
		if server.initialized.Load() != 1 || cache.Len() != 1 {
			t.Fatalf("sse=%v initialize count = %d cache = %d", sse, server.initialized.Load(), cache.Len())
		}
		if server.lastVersion != "2025-03-26" {
			t.Fatalf("negotiated protocol version not sent: %q", server.lastVersion)
		}
		// A cached session is reused.
		if _, err := mcp.Call(context.Background(), "k1", bearer("good"), "tools/call", map[string]any{"name": "search", "arguments": map[string]any{}}); err != nil {
			t.Fatal(err)
		}
		if server.initialized.Load() != 1 {
			t.Fatalf("cached session was not reused: %d", server.initialized.Load())
		}
		// An expired session (404) is dropped and re-established once.
		server.expireSessions()
		if _, err := mcp.Call(context.Background(), "k1", bearer("good"), "tools/call", map[string]any{"name": "search", "arguments": map[string]any{}}); err != nil {
			t.Fatal(err)
		}
		if server.initialized.Load() != 2 {
			t.Fatalf("expired session was not re-established: %d", server.initialized.Load())
		}
	}
}

func TestSessionClientStatelessServerNeedsNoInitialize(t *testing.T) {
	server := newSessionServer(t, false, true)
	mcp, err := server.client(t).MCP(server.URL+"/mcp/", NewSessionCache(time.Minute, 4))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := mcp.ListTools(context.Background(), "k", bearer("good"), 256); err != nil {
		t.Fatal(err)
	}
	if server.initialized.Load() != 0 {
		t.Fatalf("stateless server was initialized %d times", server.initialized.Load())
	}
	if server.lastVersion != DefaultSessionProtocolVersion {
		t.Fatalf("protocol header = %q", server.lastVersion)
	}
}

func TestSessionClientNeverRetriesToolCallAfterServerError(t *testing.T) {
	server := newSessionServer(t, false, false)
	server.status = http.StatusBadGateway
	mcp, _ := server.client(t).MCP(server.URL+"/mcp", nil)
	_, err := mcp.Call(context.Background(), "", bearer("good"), "tools/call", map[string]any{"name": "create_issue"})
	var status *StatusError
	if !errors.As(err, &status) || status.StatusCode != http.StatusBadGateway {
		t.Fatalf("err = %v", err)
	}
	if server.calls.Load() != 1 || server.initialized.Load() != 0 {
		t.Fatalf("tool call retried: calls=%d initialize=%d", server.calls.Load(), server.initialized.Load())
	}
	// 401 is returned for the caller's refresh, without retries.
	_, err = mcp.Call(context.Background(), "", bearer("stale"), "tools/call", map[string]any{"name": "search"})
	if !errors.As(err, &status) || status.StatusCode != http.StatusUnauthorized {
		t.Fatalf("401 err = %v", err)
	}
}

func TestSessionClientCapsToolList(t *testing.T) {
	server := newSessionServer(t, false, false)
	mcp, _ := server.client(t).MCP(server.URL+"/mcp", nil)
	tools, truncated, err := mcp.ListTools(context.Background(), "", bearer("good"), 2)
	if err != nil || !truncated || len(tools) != 2 {
		t.Fatalf("ListTools = %+v %v %v", tools, truncated, err)
	}
}

func TestExternalClientPinsHosts(t *testing.T) {
	server := newSessionServer(t, false, false)
	client := NewExternalClientWithTransport([]string{"mcp.example.com"}, server.Client().Transport)
	if _, err := client.MCP(server.URL+"/mcp", nil); err == nil {
		t.Fatal("endpoint outside the host allowlist accepted")
	}
	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	if _, err := client.HTTPClient().Do(request); err == nil || !strings.Contains(err.Error(), "allowlist") {
		t.Fatalf("request outside allowlist: %v", err)
	}
	for raw, want := range map[string]bool{
		"https://mcp.example.com/mcp":      true,
		"https://mcp.example.com:443/mcp":  true,
		"https://mcp.example.com:8443/mcp": false,
		"http://mcp.example.com/mcp":       false,
		"https://u@mcp.example.com/mcp":    false,
		"https://x.mcp.example.com/mcp":    false,
	} {
		if _, err := client.CheckURL(raw); (err == nil) != want {
			t.Errorf("CheckURL(%q) err=%v, want ok=%v", raw, err, want)
		}
	}
}

func TestExternalTransportRefusesNonPublicDirectAddresses(t *testing.T) {
	server := newSessionServer(t, false, false)
	u, _ := url.Parse(server.URL)
	client := NewExternalClient([]string{u.Host})
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader("{}"))
	_, err := client.HTTPClient().Do(request)
	if err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("loopback direct dial err = %v", err)
	}
}

func TestProxyDialAddressDefaults(t *testing.T) {
	for raw, want := range map[string]string{
		"http://Proxy.Corp:3128": "proxy.corp:3128",
		"http://proxy.corp":      "proxy.corp:80",
		"https://proxy.corp":     "proxy.corp:443",
	} {
		u, _ := url.Parse(raw)
		if got := proxyDialAddress(u); got != want {
			t.Errorf("proxyDialAddress(%q) = %q, want %q", raw, got, want)
		}
	}
}

// oauthServer is a fake MCP resource + authorization server on one origin.
type oauthServer struct {
	*httptest.Server
	resource       string // declared protected resource ("" = no PRM document)
	authMethods    []string
	tokenRequests  atomic.Int32
	refreshAnswer  string // "invalid_grant", "github" or "" (success)
	lastChallenge  string
	registeredWith string
}

func newOAuthServer(t *testing.T) *oauthServer {
	t.Helper()
	s := &oauthServer{authMethods: []string{"none"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if s.resource != "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="OAuth", resource_metadata="`+s.URL+`/.well-known/oauth-protected-resource"`)
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		if s.resource == "" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": s.resource, "authorization_servers": []string{s.URL}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": s.URL, "authorization_endpoint": s.URL + "/authorize", "token_endpoint": s.URL + "/token",
			"registration_endpoint": s.URL + "/register", "code_challenge_methods_supported": []string{"S256"},
			"token_endpoint_auth_methods_supported": s.authMethods,
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.registeredWith, _ = body["token_endpoint_auth_method"].(string)
		answer := map[string]any{"client_id": "client-1"}
		if s.registeredWith != "none" {
			answer["client_secret"] = "secret-1"
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(answer)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		s.tokenRequests.Add(1)
		_ = r.ParseForm()
		switch {
		case r.Form.Get("grant_type") == "refresh_token" && s.refreshAnswer == "invalid_grant":
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant", "error_description": "token " + r.Form.Get("refresh_token") + " revoked"})
			return
		case r.Form.Get("grant_type") == "refresh_token" && s.refreshAnswer == "github":
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "bad_refresh_token"})
			return
		case r.Form.Get("grant_type") == "authorization_code":
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			expected := sha256.Sum256([]byte("verifier-1"))
			if r.Form.Get("code") != "code-1" || base64.RawURLEncoding.EncodeToString(sum[:]) != base64.RawURLEncoding.EncodeToString(expected[:]) {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-2", "token_type": "bearer", "expires_in": "3600", "refresh_token": "refresh-2"})
	})
	s.Server = httptest.NewTLSServer(mux)
	t.Cleanup(s.Close)
	return s
}

func (s *oauthServer) client() *ExternalClient {
	u, _ := url.Parse(s.URL)
	return NewExternalClientWithTransport([]string{u.Host}, s.Client().Transport)
}

func TestExternalDiscoverOAuthAcceptsOriginResourceAndNoPRM(t *testing.T) {
	server := newOAuthServer(t)
	// Asana/Stripe shape: the protected resource is the origin.
	server.resource = server.URL
	metadata, err := server.client().DiscoverOAuth(context.Background(), server.URL+"/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if metadata.ResourceEndpoint != server.URL || metadata.TokenEndpoint != server.URL+"/token" || metadata.RegistrationEndpoint != server.URL+"/register" {
		t.Fatalf("metadata = %+v", metadata)
	}
	// Atlassian shape: no protected resource document at all.
	server.resource = ""
	metadata, err = server.client().DiscoverOAuth(context.Background(), server.URL+"/mcp")
	if err != nil || metadata.ResourceEndpoint != server.URL+"/mcp" || metadata.AuthorizationEndpoint != server.URL+"/authorize" {
		t.Fatalf("no-PRM metadata = %+v %v", metadata, err)
	}
	// A foreign declared resource is refused.
	server.resource = "https://other.example/mcp"
	if _, err := server.client().DiscoverOAuth(context.Background(), server.URL+"/mcp"); err == nil {
		t.Fatal("foreign protected resource accepted")
	}
}

func TestExternalDiscoverOAuthRejectsEndpointsOutsideHosts(t *testing.T) {
	server := newOAuthServer(t)
	server.resource = server.URL + "/mcp"
	client := NewExternalClientWithTransport([]string{"mcp.example.com"}, server.Client().Transport)
	if _, err := client.DiscoverOAuth(context.Background(), server.URL+"/mcp"); err == nil {
		t.Fatal("discovery outside the host allowlist succeeded")
	}
}

func TestExternalRegisterChoosesAuthMethod(t *testing.T) {
	server := newOAuthServer(t)
	metadata := OAuthMetadata{RegistrationEndpoint: server.URL + "/register", TokenAuthMethods: []string{"none", "client_secret_basic"}}
	registration, err := server.client().RegisterOAuthClient(context.Background(), metadata, "https://app.example/cb", "Multica")
	if err != nil || registration.ClientID != "client-1" || registration.TokenEndpointAuthMethod != "none" || server.registeredWith != "none" {
		t.Fatalf("public registration = %+v %v (%s)", registration, err, server.registeredWith)
	}
	metadata.TokenAuthMethods = []string{"client_secret_basic", "client_secret_post"}
	registration, err = server.client().RegisterOAuthClient(context.Background(), metadata, "https://app.example/cb", "Multica")
	if err != nil || registration.ClientSecret != "secret-1" || registration.TokenEndpointAuthMethod != "client_secret_basic" {
		t.Fatalf("confidential registration = %+v %v", registration, err)
	}
}

func TestExternalTokenErrorsAreTyped(t *testing.T) {
	server := newOAuthServer(t)
	registration := OAuthClientRegistration{ClientID: "client-1", TokenEndpointAuthMethod: "none"}
	token, err := server.client().RefreshOAuthToken(context.Background(), server.URL+"/token", "", "refresh-1", registration)
	if err != nil || token.AccessToken != "access-2" || token.ExpiresIn != 3600 || token.RefreshToken != "refresh-2" {
		t.Fatalf("refresh = %+v %v", token, err)
	}
	server.refreshAnswer = "invalid_grant"
	_, err = server.client().RefreshOAuthToken(context.Background(), server.URL+"/token", "", "refresh-secret-value", registration)
	if !IsInvalidGrant(err) || strings.Contains(err.Error(), "refresh-secret-value") {
		t.Fatalf("invalid_grant err = %v", err)
	}
	server.refreshAnswer = "github"
	if _, err = server.client().RefreshOAuthToken(context.Background(), server.URL+"/token", "", "refresh-1", registration); !IsInvalidGrant(err) {
		t.Fatalf("GitHub 200 error answer = %v", err)
	}
	if _, err = server.client().ExchangeOAuthCode(context.Background(), server.URL+"/token", "", "wrong", "https://app.example/cb", "verifier-1", registration); !IsInvalidGrant(err) {
		t.Fatalf("bad code err = %v", err)
	}
}

func TestReadRPCResponseStopsAtMatchingEvent(t *testing.T) {
	stream := "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n" +
		"data: {\"jsonrpc\":\"2.0\",\n" +
		"data: \"id\":2,\"result\":{\"ok\":true}}\n\n"
	raw, err := readRPCResponse(io.MultiReader(strings.NewReader(stream), blockingReader{}), "text/event-stream", 2)
	if err != nil || !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("readRPCResponse = %s %v", raw, err)
	}
}

// blockingReader fails the test by timing out if it is read: the reader must
// stop at the matching event instead of waiting for the stream to close.
type blockingReader struct{}

func (blockingReader) Read([]byte) (int, error) { return 0, errors.New("read past the matching event") }
