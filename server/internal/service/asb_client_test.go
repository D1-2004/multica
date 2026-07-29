package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/util"
)

const (
	testASBAPIKey     = "test-asb-api-key"
	testSandboxID     = "sandbox-123"
	testAgentToken    = "test-agent-initial-token"
	testBUCRefresh    = "test-buc-refresh-token"
	testEndpointToken = "test-endpoint-token"
)

func TestASBClientLifecycle(t *testing.T) {
	t.Parallel()

	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get(asbAPIKeyHeader); got != testASBAPIKey {
			t.Errorf("lifecycle API key = %q", got)
		}
		calls = append(calls, request.Method+" "+request.URL.RequestURI())

		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/root/v1/sandboxes":
			var payload asbCreateSandboxRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("decode create request: %v", err)
			}
			if payload.Image.URI != "registry.example/runtime@sha256:abc" {
				t.Errorf("image URI = %q", payload.Image.URI)
			}
			if payload.ResourceLimits["cpu"] != "2" || payload.ResourceLimits["memory"] != "4Gi" {
				t.Errorf("resource limits = %#v", payload.ResourceLimits)
			}
			if payload.Extensions["spiffe.lazyAuth"] != "true" ||
				payload.Extensions["wireguard.worker"] != "12345" ||
				payload.Extensions["wireguard.lazyAuth"] != "" {
				t.Errorf("identity extensions = %#v", payload.Extensions)
			}
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"sandbox-123","status":{"state":"Pending"},"createdAt":"2026-07-29T05:00:00Z","entrypoint":["tail","-f","/dev/null"]}`)
		case request.Method == http.MethodGet && request.URL.Path == "/root/v1/sandboxes/"+testSandboxID:
			response.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(response, `{"id":"sandbox-123","status":{"state":"Running"},"createdAt":"2026-07-29T05:00:00Z","entrypoint":["tail","-f","/dev/null"]}`)
		case request.Method == http.MethodPost && request.URL.Path == "/root/v1/sandboxes/"+testSandboxID+"/renew-expiration":
			var payload struct {
				ExpiresAt time.Time `json:"expiresAt"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("decode renew request: %v", err)
			}
			if payload.ExpiresAt.IsZero() {
				t.Error("renew expiration is missing")
			}
			response.WriteHeader(http.StatusOK)
		case request.Method == http.MethodDelete && request.URL.Path == "/root/v1/sandboxes/"+testSandboxID:
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client, err := NewASBClient(ASBClientConfig{
		BaseURL:    server.URL + "/root",
		APIKey:     testASBAPIKey,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewASBClient: %v", err)
	}

	sandbox, err := client.CreateSandbox(context.Background(), ASBCreateSandboxInput{
		ImageURI:       "registry.example/runtime@sha256:abc",
		TimeoutSeconds: 3600,
		ResourceCPU:    "2",
		ResourceMemory: "4Gi",
		Entrypoint:     []string{"tail", "-f", "/dev/null"},
		Extensions: map[string]string{
			"spiffe.lazyAuth":  "true",
			"wireguard.worker": "12345",
		},
	})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	if sandbox.ID != testSandboxID || sandbox.Status.State != "Pending" {
		t.Fatalf("created sandbox = %#v", sandbox)
	}

	sandbox, err = client.GetSandbox(context.Background(), testSandboxID)
	if err != nil {
		t.Fatalf("GetSandbox: %v", err)
	}
	if sandbox.Status.State != "Running" {
		t.Errorf("sandbox state = %q", sandbox.Status.State)
	}
	if err := client.RenewSandbox(context.Background(), testSandboxID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("RenewSandbox: %v", err)
	}
	if err := client.DeleteSandbox(context.Background(), testSandboxID); err != nil {
		t.Fatalf("DeleteSandbox: %v", err)
	}

	if len(calls) != 4 {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestASBClientEnforcesLifecycleLimits(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()
	client := newTestASBClient(t, server)

	input := ASBCreateSandboxInput{
		ImageURI:       "registry.example/runtime@sha256:abc",
		ResourceCPU:    "2",
		ResourceMemory: "4Gi",
		Entrypoint:     []string{"sleep infinity"},
	}
	for _, timeout := range []int{asbMinCreateTimeout - 1, asbMaxCreateTimeout + 1} {
		input.TimeoutSeconds = timeout
		if _, err := client.CreateSandbox(context.Background(), input); err == nil {
			t.Fatalf("CreateSandbox accepted timeout %d", timeout)
		}
	}
	input.TimeoutSeconds = asbMinCreateTimeout
	input.Extensions = map[string]string{
		"wireguard.lazyAuth": "true",
		"wireguard.worker":   "12345",
	}
	if _, err := client.CreateSandbox(context.Background(), input); err == nil {
		t.Fatal("CreateSandbox accepted mutually exclusive WireGuard extensions")
	}
	if err := client.RenewSandbox(
		context.Background(),
		testSandboxID,
		time.Now().Add(asbMaxRenewalDuration+time.Minute),
	); err == nil {
		t.Fatal("RenewSandbox accepted an expiration beyond seven days")
	}
}

func TestASBIdentityAnchorUsesCreateAndRenewalLimits(t *testing.T) {
	var (
		server          *httptest.Server
		createTimeout   int
		renewExpiration time.Time
		calls           []string
	)
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls = append(calls, request.Method+" "+request.URL.RequestURI())
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/sandboxes":
			var payload asbCreateSandboxRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("decode create request: %v", err)
			}
			createTimeout = payload.Timeout
			if payload.Extensions["wireguard.lazyAuth"] != "true" {
				t.Errorf("identity anchor extensions = %#v", payload.Extensions)
			}
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(response, `{"id":"sandbox-123","status":{"state":"Pending"},"createdAt":"2026-07-29T05:00:00Z","entrypoint":["sleep infinity"]}`)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/"+testSandboxID:
			response.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(response, `{"id":"sandbox-123","status":{"state":"Running"},"createdAt":"2026-07-29T05:00:00Z","entrypoint":["sleep infinity"]}`)
		case request.Method == http.MethodPost && request.URL.Path == "/v1/sandboxes/"+testSandboxID+"/identity/wireguard":
			if request.URL.Query().Get("sync") != "true" {
				t.Errorf("wireguard sync = %q", request.URL.Query().Get("sync"))
			}
			response.WriteHeader(http.StatusOK)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/"+testSandboxID+"/endpoints/44772":
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(map[string]any{
				"endpoint": server.URL + "/execd",
				"headers":  map[string]string{"X-Sandbox-Token": testEndpointToken},
			})
		case request.Method == http.MethodPost && request.URL.Path == "/execd/command":
			response.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(response, `data: {"type":"execution_complete","execution_time":1}`+"\n")
		case request.Method == http.MethodPost && request.URL.Path == "/v1/sandboxes/"+testSandboxID+"/renew-expiration":
			var payload struct {
				ExpiresAt time.Time `json:"expiresAt"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("decode renew request: %v", err)
			}
			renewExpiration = payload.ExpiresAt
			response.WriteHeader(http.StatusOK)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	manager := &ASBIdentityAnchorManager{
		Client: newTestASBClient(t, server),
		Config: ASBConfig{
			IdentityAnchorTimeout:  asbMaxRenewalDuration,
			ReadyTimeout:           time.Second,
			IdentityProbeTimeout:   time.Second,
			ResourceCPU:            "2",
			ResourceMemory:         "4Gi",
			WireGuardCredentials:   "test-wireguard",
			IdentityAnchorImageRef: "registry.example/anchor@sha256:" + strings.Repeat("a", 64),
		},
	}
	startedAt := time.Now()
	sandboxID, err := manager.Create(
		context.Background(),
		util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
		"12345",
		BUCIdentityTokens{
			AccessToken:  "test-access",
			RefreshToken: testBUCRefresh,
			IDToken:      "test-id-token",
		},
	)
	if err != nil {
		t.Fatalf("Create identity anchor: %v", err)
	}
	if sandboxID != testSandboxID {
		t.Fatalf("sandbox ID = %q", sandboxID)
	}
	if createTimeout != asbMaxCreateTimeout {
		t.Fatalf("create timeout = %d, want %d", createTimeout, asbMaxCreateTimeout)
	}
	earliestRenewal := startedAt.Add(asbMaxRenewalDuration - time.Minute)
	latestRenewal := time.Now().Add(asbMaxRenewalDuration + time.Minute)
	if renewExpiration.Before(earliestRenewal) || renewExpiration.After(latestRenewal) {
		t.Fatalf("renew expiration = %s, want approximately seven days", renewExpiration)
	}
	if got := strings.Join(calls, "\n"); !strings.HasSuffix(got, "POST /v1/sandboxes/"+testSandboxID+"/renew-expiration") {
		t.Fatalf("renewal was not the final lifecycle call:\n%s", got)
	}
}

func TestASBClientIdentityInjection(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get(asbAPIKeyHeader) != testASBAPIKey {
			t.Error("lifecycle API key is missing")
		}
		switch request.URL.Path {
		case "/v1/sandboxes/" + testSandboxID + "/identity/spiffe":
			if request.URL.Query().Get("sync") != "false" {
				t.Errorf("spiffe sync = %q", request.URL.Query().Get("sync"))
			}
			var grant ASBAgentIdentityGrant
			if err := json.NewDecoder(request.Body).Decode(&grant); err != nil {
				t.Fatalf("decode Agent Identity grant: %v", err)
			}
			if grant.RawEmployeeID != "12345" || grant.AgentToken != testAgentToken || grant.AgentID != "spiffe://agent/test" {
				t.Errorf("Agent Identity grant = %#v", grant)
			}
		case "/v1/sandboxes/" + testSandboxID + "/identity/wireguard":
			if request.URL.Query().Get("sync") != "true" {
				t.Errorf("wireguard sync = %q", request.URL.Query().Get("sync"))
			}
			var grant ASBBUCIdentityGrant
			if err := json.NewDecoder(request.Body).Decode(&grant); err != nil {
				t.Fatalf("decode BUC grant: %v", err)
			}
			if grant.EmployeeID != "12345" || grant.BUCRefreshToken != testBUCRefresh || grant.OriginalSandboxID != "anchor-1" {
				t.Errorf("BUC grant = %#v", grant)
			}
		default:
			http.NotFound(response, request)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := newTestASBClient(t, server)
	if err := client.AttachAgentIdentity(context.Background(), testSandboxID, ASBAgentIdentityGrant{
		RawEmployeeID: "12345",
		AgentToken:    testAgentToken,
		AgentID:       "spiffe://agent/test",
	}); err != nil {
		t.Fatalf("AttachAgentIdentity: %v", err)
	}
	if err := client.AttachBUCIdentity(context.Background(), testSandboxID, ASBBUCIdentityGrant{
		EmployeeID:           "12345",
		BUCAccessToken:       "test-buc-access",
		BUCRefreshToken:      testBUCRefresh,
		BUCIDToken:           "test-buc-id",
		WireGuardCredentials: "test-wireguard",
		OriginalSandboxID:    "anchor-1",
	}, true); err != nil {
		t.Fatalf("AttachBUCIdentity: %v", err)
	}
}

func TestASBClientExecUsesOnlyEndpointHeaders(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/sandboxes/" + testSandboxID + "/endpoints/44772":
			if request.Header.Get(asbAPIKeyHeader) != testASBAPIKey {
				t.Error("lifecycle API key is missing")
			}
			_ = json.NewEncoder(response).Encode(map[string]any{
				"endpoint": strings.TrimPrefix(serverURLFromRequest(request), "http://") + "/execd",
				"headers":  map[string]string{"X-Sandbox-Token": testEndpointToken},
			})
		case "/execd/command":
			if request.Header.Get(asbAPIKeyHeader) != "" {
				t.Error("lifecycle API key leaked to execd")
			}
			if request.Header.Get("X-Sandbox-Token") != testEndpointToken {
				t.Error("sandbox endpoint header is missing")
			}
			var payload asbExecRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("decode exec request: %v", err)
			}
			if payload.Command != "/usr/local/bin/multica-fc-hermes-container-log-entry --version" ||
				payload.CWD != "/workspace" ||
				payload.Timeout != 5000 ||
				payload.Envs["SSL_CERT_FILE"] != "/etc/ssl/certs/ca-certificates.crt" {
				t.Errorf("exec request = %#v", payload)
			}
			response.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(response, "event: message\n")
			_, _ = io.WriteString(response, `data: {"type":"ping","timestamp":0}`+"\n")
			_, _ = io.WriteString(response, `data: {"type":"init","text":"exec-1","timestamp":1}`+"\n")
			_, _ = io.WriteString(response, `data: {"type":"stdout","text":"version 1\n","timestamp":2}`+"\n")
			_, _ = io.WriteString(response, `data: {"type":"stderr","text":"warning\n","timestamp":3}`+"\n")
			_, _ = io.WriteString(response, `data: {"type":"result","results":{"text":"done"},"timestamp":4}`+"\n")
			_, _ = io.WriteString(response, `data: {"type":"execution_complete","execution_time":12,"timestamp":5}`+"\n")
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client := newTestASBClient(t, server)
	endpoint, err := client.GetEndpoint(context.Background(), testSandboxID, asbExecPort)
	if err != nil {
		t.Fatalf("GetEndpoint: %v", err)
	}
	result, err := client.Exec(context.Background(), endpoint, ASBExecInput{
		Command: "/usr/local/bin/multica-fc-hermes-container-log-entry --version",
		CWD:     "/workspace",
		Timeout: 5 * time.Second,
		Envs: map[string]string{
			"SSL_CERT_FILE": "/etc/ssl/certs/ca-certificates.crt",
		},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.ExecutionID != "exec-1" ||
		result.Stdout != "version 1\n" ||
		result.Stderr != "warning\n" ||
		result.Result != "done" ||
		result.ExitCode == nil ||
		*result.ExitCode != 0 {
		t.Fatalf("exec result = %#v", result)
	}
}

func TestASBClientDoesNotExposeSecretResponseBodies(t *testing.T) {
	t.Parallel()

	secret := "server-echoed-secret"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("X-Request-ID", "request-123")
		response.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(response, `{"message":"`+secret+`","token":"`+testAgentToken+`"}`)
	}))
	defer server.Close()

	client := newTestASBClient(t, server)
	_, err := client.GetSandbox(context.Background(), testSandboxID)
	if err == nil {
		t.Fatal("GetSandbox unexpectedly succeeded")
	}
	message := err.Error()
	for _, forbidden := range []string{secret, testAgentToken, testASBAPIKey} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("error leaks secret %q: %s", forbidden, message)
		}
	}
	if !strings.Contains(message, "HTTP 401") || !strings.Contains(message, "request-123") {
		t.Fatalf("sanitized error = %q", message)
	}
}

func TestASBClientRejectsUnsafeInputsAndStreams(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"",
		"ftp://sandbox.example",
		"https://user:secret@sandbox.example",
		"https://sandbox.example/path?token=secret",
	} {
		_, err := NewASBClient(ASBClientConfig{BaseURL: raw, APIKey: testASBAPIKey})
		if err == nil {
			t.Errorf("NewASBClient(%q) unexpectedly succeeded", raw)
		}
	}

	client, err := NewASBClient(ASBClientConfig{BaseURL: "https://sandbox.example", APIKey: testASBAPIKey})
	if err != nil {
		t.Fatalf("NewASBClient: %v", err)
	}
	if _, err := client.GetSandbox(context.Background(), "../other"); !errors.Is(err, ErrASBInvalidSandboxID) {
		t.Fatalf("invalid sandbox ID error = %v", err)
	}

	_, err = decodeASBExecStream(strings.NewReader(`data: {"type":"credential","text":"no","timestamp":1}` + "\n"))
	if err == nil || !strings.Contains(err.Error(), "unsupported event type") {
		t.Fatalf("unsupported event error = %v", err)
	}
	_, err = decodeASBExecStream(strings.NewReader("data: not-json\n"))
	if err == nil || !strings.Contains(err.Error(), "malformed SSE") {
		t.Fatalf("malformed event error = %v", err)
	}
}

func TestASBClientDoesNotFollowEndpointRedirect(t *testing.T) {
	t.Parallel()

	leaked := false
	destination := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Sandbox-Token") != "" {
			leaked = true
		}
	}))
	defer destination.Close()

	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/command" {
			http.Redirect(response, request, destination.URL, http.StatusTemporaryRedirect)
			return
		}
		http.NotFound(response, request)
	}))
	defer source.Close()

	client, err := NewASBClient(ASBClientConfig{
		BaseURL:    source.URL,
		APIKey:     testASBAPIKey,
		HTTPClient: source.Client(),
	})
	if err != nil {
		t.Fatalf("NewASBClient: %v", err)
	}
	endpointURL, _ := urlParseForTest(source.URL)
	_, err = client.Exec(context.Background(), &ASBEndpoint{
		URL: endpointURL,
		Headers: http.Header{
			"X-Sandbox-Token": []string{testEndpointToken},
		},
	}, ASBExecInput{Command: "true"})
	if err == nil {
		t.Fatal("Exec unexpectedly followed redirect")
	}
	if leaked {
		t.Fatal("endpoint credential leaked across redirect")
	}
}

func newTestASBClient(t *testing.T, server *httptest.Server) *ASBClient {
	t.Helper()
	client, err := NewASBClient(ASBClientConfig{
		BaseURL:    server.URL,
		APIKey:     testASBAPIKey,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewASBClient: %v", err)
	}
	return client
}

func serverURLFromRequest(request *http.Request) string {
	return "http://" + request.Host
}

func urlParseForTest(raw string) (*url.URL, error) {
	return url.Parse(raw)
}
