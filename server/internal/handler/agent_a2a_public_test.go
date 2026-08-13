package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/internal/featureflags"
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestAgentA2APublicRoutesCamouflageUnsafeRuntime(t *testing.T) {
	tests := []struct {
		name        string
		appEnv      string
		allowUnsafe string
		publicURL   string
	}{
		{
			name:      "override absent",
			appEnv:    "development",
			publicURL: "http://127.0.0.1:8080",
		},
		{
			name:        "production override",
			appEnv:      "production",
			allowUnsafe: "true",
			publicURL:   "http://127.0.0.1:8080",
		},
		{
			name:        "non-loopback override",
			appEnv:      "development",
			allowUnsafe: "true",
			publicURL:   "https://agents.example.test",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("APP_ENV", test.appEnv)
			t.Setenv("AONE_ENV_TYPE", "")
			t.Setenv("ENV_TYPE", "")
			t.Setenv("GO_ENV", "")
			t.Setenv(agentA2AAllowUnsafeLocalRuntimeEnv, test.allowUnsafe)
			t.Setenv(agentA2AAllowUnsafePrereleaseRuntimeEnv, "")
			t.Setenv(agentA2AUnsafePrereleasePublicURLEnv, "")
			h := &Handler{cfg: Config{PublicURL: test.publicURL}}
			withFeatureFlag(t, h, featureflags.AgentA2AInbound, true)

			cardResponse := httptest.NewRecorder()
			h.GetAgentA2ACard(cardResponse, httptest.NewRequest(http.MethodGet, "/api/a2a/agents/opaque/.well-known/agent-card.json", nil))
			if cardResponse.Code != http.StatusNotFound {
				t.Fatalf("Agent Card status = %d, want camouflage 404: %s", cardResponse.Code, cardResponse.Body.String())
			}

			rpcResponse := httptest.NewRecorder()
			h.HandleAgentA2ARPC(rpcResponse, httptest.NewRequest(http.MethodPost, "/api/a2a/agents/opaque/v1", nil))
			if rpcResponse.Code != http.StatusNotFound {
				t.Fatalf("RPC status = %d, want camouflage 404: %s", rpcResponse.Code, rpcResponse.Body.String())
			}
		})
	}
}

func TestAgentA2APublicRoutesRequireLoopbackSocketPeer(t *testing.T) {
	allowUnsafeLocalAgentA2ARuntimeForTest(t)
	database := &agentA2ARecordingDB{}
	h := &Handler{
		cfg:     Config{PublicURL: "http://127.0.0.1:8080"},
		Queries: db.New(database),
	}
	withFeatureFlag(t, h, featureflags.AgentA2AInbound, true)

	lanCardRequest := httptest.NewRequest(http.MethodGet, "/api/a2a/agents/opaque/.well-known/agent-card.json", nil)
	lanCardRequest.RemoteAddr = "192.168.1.20:49152"
	lanCardRequest.Header.Set("X-Forwarded-For", "127.0.0.1")
	lanCardResponse := httptest.NewRecorder()
	h.GetAgentA2ACard(lanCardResponse, lanCardRequest)
	if lanCardResponse.Code != http.StatusNotFound || database.queryRowCalls != 0 {
		t.Fatalf("LAN Card request = status:%d queries:%d, want 404 before DB", lanCardResponse.Code, database.queryRowCalls)
	}

	lanRPCRequest := httptest.NewRequest(http.MethodPost, "/api/a2a/agents/opaque/v1", nil)
	lanRPCRequest.RemoteAddr = "192.168.1.20:49152"
	lanRPCRequest.Header.Set("X-Forwarded-For", "127.0.0.1")
	lanRPCResponse := httptest.NewRecorder()
	h.HandleAgentA2ARPC(lanRPCResponse, lanRPCRequest)
	if lanRPCResponse.Code != http.StatusNotFound {
		t.Fatalf("LAN RPC status = %d, want camouflage 404", lanRPCResponse.Code)
	}

	loopbackCardRequest := httptest.NewRequest(http.MethodGet, "/api/a2a/agents/opaque/.well-known/agent-card.json", nil)
	loopbackCardRequest.RemoteAddr = "127.0.0.1:49152"
	loopbackCardRequest.Header.Set("X-Forwarded-For", "203.0.113.8")
	loopbackCardResponse := httptest.NewRecorder()
	h.GetAgentA2ACard(loopbackCardResponse, loopbackCardRequest)
	if loopbackCardResponse.Code != http.StatusNotFound || database.queryRowCalls != 1 {
		t.Fatalf("loopback Card request = status:%d queries:%d, want request to pass safety gate and query once", loopbackCardResponse.Code, database.queryRowCalls)
	}

	loopbackRPCRequest := httptest.NewRequest(http.MethodPost, "/api/a2a/agents/opaque/v1", nil)
	loopbackRPCRequest.RemoteAddr = "127.0.0.1:49152"
	loopbackRPCRequest.Header.Set("X-Forwarded-For", "203.0.113.8")
	loopbackRPCResponse := httptest.NewRecorder()
	h.HandleAgentA2ARPC(loopbackRPCResponse, loopbackRPCRequest)
	if loopbackRPCResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("loopback RPC status = %d, want 503 proving request passed safety gate", loopbackRPCResponse.Code)
	}
}

func TestAgentA2APublicRoutesAllowPrereleaseRemoteSocketPeer(t *testing.T) {
	const publicURL = "https://agents.example.test/a2a"
	allowUnsafePrereleaseAgentA2ARuntimeForTest(t, publicURL)
	database := &agentA2ARecordingDB{}
	h := &Handler{
		cfg:     Config{PublicURL: publicURL},
		Queries: db.New(database),
	}
	withFeatureFlag(t, h, featureflags.AgentA2AInbound, true)

	cardRequest := httptest.NewRequest(http.MethodGet, "/api/a2a/agents/opaque/.well-known/agent-card.json", nil)
	cardRequest.RemoteAddr = "10.20.30.40:49152"
	cardResponse := httptest.NewRecorder()
	h.GetAgentA2ACard(cardResponse, cardRequest)
	if cardResponse.Code != http.StatusNotFound || database.queryRowCalls != 1 {
		t.Fatalf("prerelease Card request = status:%d queries:%d, want request to pass safety gate and query once", cardResponse.Code, database.queryRowCalls)
	}

	rpcRequest := httptest.NewRequest(http.MethodPost, "/api/a2a/agents/opaque/v1", nil)
	rpcRequest.RemoteAddr = "10.20.30.40:49152"
	rpcResponse := httptest.NewRecorder()
	h.HandleAgentA2ARPC(rpcResponse, rpcRequest)
	if rpcResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("prerelease RPC status = %d, want 503 proving remote peer passed safety gate", rpcResponse.Code)
	}
}

type agentA2ARecordingDB struct {
	queryRowCalls int
}

func (*agentA2ARecordingDB) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	panic("unexpected Exec")
}

func (*agentA2ARecordingDB) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	panic("unexpected Query")
}

func (database *agentA2ARecordingDB) QueryRow(context.Context, string, ...interface{}) pgx.Row {
	database.queryRowCalls++
	return agentA2AErrorRow{err: pgx.ErrNoRows}
}

type agentA2AErrorRow struct {
	err error
}

func (row agentA2AErrorRow) Scan(...interface{}) error {
	return row.err
}

func TestParseAgentA2ABearer(t *testing.T) {
	valid := "mca2a_0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		name   string
		header string
		ok     bool
	}{
		{name: "valid", header: "Bearer " + valid, ok: true},
		{name: "case insensitive scheme", header: "bearer " + valid, ok: true},
		{name: "missing", header: ""},
		{name: "wrong prefix", header: "Bearer mul_0123456789abcdef0123456789abcdef01234567"},
		{name: "wrong length", header: "Bearer mca2a_abc"},
		{name: "non hex", header: "Bearer mca2a_zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
		{name: "extra field", header: "Bearer " + valid + " extra"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parseAgentA2ABearer(test.header)
			if ok != test.ok {
				t.Fatalf("parseAgentA2ABearer(%q) ok = %v, want %v", test.header, ok, test.ok)
			}
			if test.ok && got != valid {
				t.Fatalf("token = %q, want %q", got, valid)
			}
		})
	}
}

func TestParseAgentAccessTokenSupportsSimpleHeadersAndCapabilityPath(t *testing.T) {
	valid := "mca2a_0123456789abcdef0123456789abcdef01234567"

	for _, configure := range []func(*http.Request){
		func(request *http.Request) { request.Header.Set("Authorization", "Bearer "+valid) },
		func(request *http.Request) { request.Header.Set("X-API-Key", valid) },
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/mcp/agents/agent", nil)
		configure(request)
		if got, ok := parseAgentAccessToken(request, ""); !ok || got != valid {
			t.Fatalf("header token = %q, %v; want %q, true", got, ok, valid)
		}
	}

	request := httptest.NewRequest(http.MethodPost, "/api/mcp/connect/secret", nil)
	if got, ok := parseAgentAccessToken(request, valid); !ok || got != valid {
		t.Fatalf("path token = %q, %v; want %q, true", got, ok, valid)
	}

	request.Header.Set("X-API-Key", "mca2a_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if _, ok := parseAgentAccessToken(request, valid); ok {
		t.Fatal("conflicting credentials were accepted")
	}
}

func TestParseAgentAccessTokenRejectsDuplicateAndConflictingHeaders(t *testing.T) {
	valid := "mca2a_0123456789abcdef0123456789abcdef01234567"
	request := httptest.NewRequest(http.MethodPost, "/api/a2a/agents/agent/v1", nil)
	request.Header.Add("Authorization", "Bearer "+valid)
	request.Header.Add("Authorization", "Bearer "+valid)
	if _, ok := parseAgentAccessToken(request, ""); ok {
		t.Fatal("duplicate Authorization headers were accepted")
	}

	request = httptest.NewRequest(http.MethodPost, "/api/a2a/agents/agent/v1", nil)
	request.Header.Set("Authorization", "Bearer "+valid)
	request.Header.Set("X-API-Key", "mca2a_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if _, ok := parseAgentAccessToken(request, ""); ok {
		t.Fatal("conflicting Authorization and X-API-Key headers were accepted")
	}
}

func TestParseAgentA2AInvocationIdentity(t *testing.T) {
	const token = "opaque-external-context"
	const expires = "4102444800000"
	valid := httptest.NewRequest(http.MethodPost, "/v1", nil)
	valid.Header.Set("A2A-Extensions", "urn:example:other:v1, "+a2aintegration.AgentIdentityExtensionURI)
	valid.Header.Set(a2aintegration.AgentIdentityTokenHeader, token)
	valid.Header.Set(a2aintegration.AgentIdentityExpiryHeader, expires)
	identity, ok := parseAgentA2AInvocationIdentity(valid)
	if !ok || !identity.ExtensionDeclared || identity.ContextToken != token || identity.ExpiresAtUnixMS != 4102444800000 {
		t.Fatalf("parsed identity = %#v, ok=%v", identity, ok)
	}

	deap := httptest.NewRequest(http.MethodPost, "/v1", nil)
	deap.Header.Set(a2aintegration.DEAPDWSTokenHeader, "opaque-deap-dws-token")
	identity, ok = parseAgentA2AInvocationIdentity(deap)
	if !ok || identity.DEAPDWSToken != "opaque-deap-dws-token" || identity.ContextToken != "" {
		t.Fatalf("parsed DEAP identity = %#v, ok=%v", identity, ok)
	}

	tests := []struct {
		name      string
		configure func(*http.Request)
	}{
		{
			name: "token without extension",
			configure: func(request *http.Request) {
				request.Header.Set(a2aintegration.AgentIdentityTokenHeader, token)
				request.Header.Set(a2aintegration.AgentIdentityExpiryHeader, expires)
			},
		},
		{
			name: "token without expiry",
			configure: func(request *http.Request) {
				request.Header.Set("A2A-Extensions", a2aintegration.AgentIdentityExtensionURI)
				request.Header.Set(a2aintegration.AgentIdentityTokenHeader, token)
			},
		},
		{
			name: "expiry without token",
			configure: func(request *http.Request) {
				request.Header.Set("A2A-Extensions", a2aintegration.AgentIdentityExtensionURI)
				request.Header.Set(a2aintegration.AgentIdentityExpiryHeader, expires)
			},
		},
		{
			name: "whitespace",
			configure: func(request *http.Request) {
				request.Header.Set("A2A-Extensions", a2aintegration.AgentIdentityExtensionURI)
				request.Header.Set(a2aintegration.AgentIdentityTokenHeader, " "+token)
				request.Header.Set(a2aintegration.AgentIdentityExpiryHeader, expires)
			},
		},
		{
			name: "duplicate token",
			configure: func(request *http.Request) {
				request.Header.Set("A2A-Extensions", a2aintegration.AgentIdentityExtensionURI)
				request.Header.Add(a2aintegration.AgentIdentityTokenHeader, token)
				request.Header.Add(a2aintegration.AgentIdentityTokenHeader, token)
				request.Header.Set(a2aintegration.AgentIdentityExpiryHeader, expires)
			},
		},
		{
			name: "invalid expiry",
			configure: func(request *http.Request) {
				request.Header.Set("A2A-Extensions", a2aintegration.AgentIdentityExtensionURI)
				request.Header.Set(a2aintegration.AgentIdentityTokenHeader, token)
				request.Header.Set(a2aintegration.AgentIdentityExpiryHeader, "tomorrow")
			},
		},
		{
			name: "empty DEAP DWS token",
			configure: func(request *http.Request) {
				request.Header.Set(a2aintegration.DEAPDWSTokenHeader, "")
			},
		},
		{
			name: "whitespace DEAP DWS token",
			configure: func(request *http.Request) {
				request.Header.Set(a2aintegration.DEAPDWSTokenHeader, " opaque-deap-dws-token")
			},
		},
		{
			name: "duplicate DEAP DWS token",
			configure: func(request *http.Request) {
				request.Header.Add(a2aintegration.DEAPDWSTokenHeader, "opaque-deap-dws-token")
				request.Header.Add(a2aintegration.DEAPDWSTokenHeader, "opaque-deap-dws-token")
			},
		},
		{
			name: "conflicting DEAP and ContextToken identities",
			configure: func(request *http.Request) {
				request.Header.Set("A2A-Extensions", a2aintegration.AgentIdentityExtensionURI)
				request.Header.Set(a2aintegration.AgentIdentityTokenHeader, token)
				request.Header.Set(a2aintegration.AgentIdentityExpiryHeader, expires)
				request.Header.Set(a2aintegration.DEAPDWSTokenHeader, "opaque-deap-dws-token")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1", nil)
			test.configure(request)
			if identity, ok := parseAgentA2AInvocationIdentity(request); ok {
				t.Fatalf("invalid identity was accepted: %#v", identity)
			}
		})
	}
}

func TestAgentA2ADetectsTrailingJSONValues(t *testing.T) {
	valid := `{"jsonrpc":"2.0","id":"1","method":"SendMessage"}`
	for _, body := range []string{valid + ` {}`, valid + ` null`, valid + ` trailing`} {
		if !agentA2AHasTrailingJSONValue([]byte(body)) {
			t.Fatalf("agentA2AHasTrailingJSONValue(%q) = false, want true", body)
		}
	}
	for _, body := range []string{valid, valid + "  \n\t", `{}`, `not-json`} {
		if agentA2AHasTrailingJSONValue([]byte(body)) {
			t.Fatalf("agentA2AHasTrailingJSONValue(%q) = true, want false", body)
		}
	}
}

func TestRequestETagMatches(t *testing.T) {
	target := `"abc"`
	for _, header := range []string{`"abc"`, `W/"abc"`, `"other", "abc"`, "*"} {
		if !requestETagMatches(header, target) {
			t.Fatalf("header %q did not match", header)
		}
	}
	if requestETagMatches(`"other"`, target) {
		t.Fatal("unrelated ETag matched")
	}
}
