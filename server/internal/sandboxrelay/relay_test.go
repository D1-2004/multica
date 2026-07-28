package sandboxrelay

import (
	"context"
	"crypto/ed25519"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestRelayLeavesRequestsWithoutHeaderOnOriginalProductionPath(t *testing.T) {
	relay, _, _ := testRelay(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("upstream must not receive a request without a relay token")
	}), nil)
	var localCalls atomic.Int32
	handler := relay.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		localCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/daemon/runtimes/runtime-1/tasks/claim", nil)
	req.Header.Set("Authorization", "Bearer mdt_old-production-sandbox")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent || localCalls.Load() != 1 {
		t.Fatalf("request did not remain local: status=%d local_calls=%d", recorder.Code, localCalls.Load())
	}
}

func TestRelayForwardsBoundDaemonRequestAndStripsRoutingIdentity(t *testing.T) {
	const daemonToken = "mdt_prepub-daemon"
	upstream := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/daemon/runtimes/runtime-1/tasks/claim" || req.URL.RawQuery != "cold=true" {
			t.Errorf("unexpected upstream URL: %s", req.URL.String())
		}
		if got := req.Header.Get("Authorization"); got != "Bearer "+daemonToken {
			t.Errorf("Authorization = %q", got)
		}
		if got := req.Header.Get(protocol.SandboxRelayTokenHeader); got != "" {
			t.Errorf("relay token leaked upstream")
		}
		for _, header := range []string{"X-User-ID", "X-Actor-Source", "X-Real-IP"} {
			if got := req.Header.Get(header); got != "" {
				t.Errorf("%s leaked upstream: %q", header, got)
			}
		}
		if got := req.Header.Get("X-Forwarded-For"); got != "" {
			t.Errorf("X-Forwarded-For leaked upstream: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	relay, signer, _ := testRelay(t, upstream, nil)
	token := mintTestToken(t, signer, daemonToken, "")
	var localCalls atomic.Int32
	handler := relay.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		localCalls.Add(1)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/daemon/runtimes/runtime-1/tasks/claim?cold=true", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+daemonToken)
	req.Header.Set(protocol.SandboxRelayTokenHeader, token)
	req.Header.Set("X-User-ID", "forged")
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Real-IP", "198.51.100.10")
	req.Header.Set("X-Forwarded-For", "198.51.100.10")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK || localCalls.Load() != 0 {
		t.Fatalf("status=%d local_calls=%d body=%s", recorder.Code, localCalls.Load(), recorder.Body.String())
	}
}

func TestRelayRejectsInvalidOrMismatchedAssertionsWithoutLocalFallback(t *testing.T) {
	const daemonToken = "mdt_prepub-daemon"
	relay, signer, _ := testRelay(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("rejected request reached upstream")
	}), nil)
	valid := mintTestToken(t, signer, daemonToken, "")
	tests := []struct {
		name   string
		token  string
		bearer string
		status int
	}{
		{name: "invalid signature", token: valid + "corrupt", bearer: daemonToken, status: http.StatusUnauthorized},
		{name: "different daemon token", token: valid, bearer: "mdt_other-daemon", status: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var localCalls atomic.Int32
			handler := relay.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				localCalls.Add(1)
			}))
			req := httptest.NewRequest(http.MethodPost, "/api/daemon/runtimes/runtime-1/tasks/claim", nil)
			req.Header.Set("Authorization", "Bearer "+test.bearer)
			req.Header.Set(protocol.SandboxRelayTokenHeader, test.token)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != test.status || localCalls.Load() != 0 {
				t.Fatalf("status=%d local_calls=%d", recorder.Code, localCalls.Load())
			}
		})
	}
}

func TestRelayForwardsTaskScopedMulticaRequests(t *testing.T) {
	upstreamCalls := atomic.Int32{}
	upstream := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamCalls.Add(1)
		if got := req.Header.Get("X-Task-ID"); got != "task-1" {
			t.Errorf("X-Task-ID = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	relay, signer, _ := testRelay(t, upstream, nil)
	token := mintTestToken(t, signer, "mdt_prepub-daemon", "")

	for _, test := range []struct {
		name   string
		taskID string
		status int
	}{
		{name: "matching task", taskID: "task-1", status: http.StatusNoContent},
		{name: "mismatched task", taskID: "task-other", status: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/issues/issue-1", nil)
			req.Header.Set("Authorization", "Bearer mat_task-token")
			req.Header.Set("X-Task-ID", test.taskID)
			req.Header.Set("X-Agent-ID", "agent-1")
			req.Header.Set(protocol.SandboxRelayTokenHeader, token)
			recorder := httptest.NewRecorder()
			relay.Middleware(http.NotFoundHandler()).ServeHTTP(recorder, req)
			if recorder.Code != test.status {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("upstream calls = %d", upstreamCalls.Load())
	}
}

func TestRelayForwardsOnlyExactBoundAgentIdentityRedeem(t *testing.T) {
	const contextToken = "context-prepub-secret"
	var identityCalls atomic.Int32
	identityUpstream := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		identityCalls.Add(1)
		if req.URL.Path != agentIdentityRedeemPath {
			t.Errorf("path = %q", req.URL.Path)
		}
		if got := req.Header.Get(protocol.SandboxRelayTokenHeader); got != "" {
			t.Error("relay token leaked to Agent Identity")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	relay, signer, _ := testRelay(t, nil, identityUpstream)
	valid := mintTestToken(t, signer, "mdt_prepub-daemon", contextToken)

	tests := []struct {
		name   string
		method string
		path   string
		bearer string
		status int
	}{
		{name: "valid", method: http.MethodPost, path: agentIdentityRedeemPath, bearer: contextToken, status: http.StatusNoContent},
		{name: "wrong token", method: http.MethodPost, path: agentIdentityRedeemPath, bearer: "other-context", status: http.StatusForbidden},
		{name: "query rejected", method: http.MethodPost, path: agentIdentityRedeemPath + "?redirect=1", bearer: contextToken, status: http.StatusForbidden},
		{name: "method rejected", method: http.MethodGet, path: agentIdentityRedeemPath, bearer: contextToken, status: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, test.path, nil)
			req.Header.Set("Authorization", "Bearer "+test.bearer)
			req.Header.Set(protocol.SandboxRelayTokenHeader, valid)
			recorder := httptest.NewRecorder()
			relay.Middleware(http.NotFoundHandler()).ServeHTTP(recorder, req)
			if recorder.Code != test.status {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
	if identityCalls.Load() != 1 {
		t.Fatalf("identity upstream calls = %d", identityCalls.Load())
	}
}

func TestRelayProxiesDaemonWebSocket(t *testing.T) {
	const daemonToken = "mdt_prepub-daemon"
	upgrader := websocket.Upgrader{}
	upstream := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if got := req.Header.Get(protocol.SandboxRelayTokenHeader); got != "" {
			t.Error("relay token leaked to websocket upstream")
		}
		connection, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer connection.Close()
		messageType, payload, err := connection.ReadMessage()
		if err != nil {
			return
		}
		_ = connection.WriteMessage(messageType, payload)
	})
	relay, signer, _ := testRelay(t, upstream, nil)
	token := mintTestToken(t, signer, daemonToken, "")
	server := httptest.NewServer(relay.Middleware(http.NotFoundHandler()))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/daemon/ws"
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+daemonToken)
	headers.Set(protocol.SandboxRelayTokenHeader, token)

	connection, response, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		if response != nil {
			t.Fatalf("dial websocket: %v status=%d", err, response.StatusCode)
		}
		t.Fatalf("dial websocket: %v", err)
	}
	defer connection.Close()
	if err := connection.WriteMessage(websocket.TextMessage, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	_, payload, err := connection.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "hello" {
		t.Fatalf("payload = %q", payload)
	}
}

func TestRelayReturnsBadGatewayWithoutProcessingLocally(t *testing.T) {
	publicKey, privateKey := testKeyPair(t)
	signer, _ := NewSigner("key-1", privateKey)
	verifier, _ := NewVerifier(map[string]ed25519.PublicKey{"key-1": publicKey})
	unreachable, err := url.Parse("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	relay, err := NewRelay(RelayConfig{
		Verifier:              verifier,
		MulticaUpstream:       unreachable,
		AgentIdentityUpstream: unreachable,
		MaxInflight:           1,
	})
	if err != nil {
		t.Fatal(err)
	}
	token := mintTestToken(t, signer, "mdt_prepub-daemon", "")
	var localCalls atomic.Int32
	req := httptest.NewRequest(http.MethodPost, "/api/daemon/runtimes/runtime-1/tasks/claim", nil)
	req.Header.Set("Authorization", "Bearer mdt_prepub-daemon")
	req.Header.Set(protocol.SandboxRelayTokenHeader, token)
	recorder := httptest.NewRecorder()

	relay.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		localCalls.Add(1)
	})).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadGateway || localCalls.Load() != 0 {
		t.Fatalf("status=%d local_calls=%d", recorder.Code, localCalls.Load())
	}
}

func testRelay(t *testing.T, multicaHandler, identityHandler http.Handler) (*Relay, *Signer, *Verifier) {
	t.Helper()
	if multicaHandler == nil {
		multicaHandler = http.NotFoundHandler()
	}
	if identityHandler == nil {
		identityHandler = http.NotFoundHandler()
	}
	multicaServer := httptest.NewServer(multicaHandler)
	t.Cleanup(multicaServer.Close)
	identityServer := httptest.NewServer(identityHandler)
	t.Cleanup(identityServer.Close)
	multicaURL, _ := url.Parse(multicaServer.URL)
	identityURL, _ := url.Parse(identityServer.URL)
	publicKey, privateKey := testKeyPair(t)
	signer, err := NewSigner("key-1", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(map[string]ed25519.PublicKey{"key-1": publicKey})
	if err != nil {
		t.Fatal(err)
	}
	relay, err := NewRelay(RelayConfig{
		Verifier:              verifier,
		MulticaUpstream:       multicaURL,
		AgentIdentityUpstream: identityURL,
		MaxInflight:           8,
	})
	if err != nil {
		t.Fatal(err)
	}
	return relay, signer, verifier
}

func mintTestToken(t *testing.T, signer *Signer, daemonToken, contextToken string) string {
	t.Helper()
	raw, err := signer.Mint(MintRequest{
		TaskID:             "task-1",
		AgentID:            "agent-1",
		RuntimeID:          "runtime-1",
		SandboxID:          "sandbox-1",
		DaemonToken:        daemonToken,
		AgentIdentityToken: contextToken,
		ExpiresAt:          time.Now().Add(10 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRelayCapacityIsBounded(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	upstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	})
	relay, signer, verifier := testRelay(t, upstream, nil)
	relay, err := NewRelay(RelayConfig{
		Verifier:              verifier,
		MulticaUpstream:       relay.multicaDirectorURLForTest(t),
		AgentIdentityUpstream: relay.agentIdentityDirectorURLForTest(t),
		MaxInflight:           1,
	})
	if err != nil {
		t.Fatal(err)
	}
	token := mintTestToken(t, signer, "mdt_prepub-daemon", "")
	handler := relay.Middleware(http.NotFoundHandler())
	firstDone := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/daemon/runtimes/runtime-1/tasks/claim", nil)
		req.Header.Set("Authorization", "Bearer mdt_prepub-daemon")
		req.Header.Set(protocol.SandboxRelayTokenHeader, token)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		firstDone <- recorder.Code
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first request did not reach upstream")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/daemon/runtimes/runtime-1/tasks/claim", nil)
	req.Header.Set("Authorization", "Bearer mdt_prepub-daemon")
	req.Header.Set(protocol.SandboxRelayTokenHeader, token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("second request status=%d", recorder.Code)
	}
	close(release)
	select {
	case status := <-firstDone:
		if status != http.StatusNoContent {
			t.Fatalf("first request status=%d", status)
		}
	case <-time.After(time.Second):
		t.Fatal("first request did not complete")
	}
}

// The helpers below extract the fixed origins installed by NewSingleHostReverseProxy
// without issuing a request. They are test-only and keep the capacity test on
// the same upstream servers created by testRelay.
func (relay *Relay) multicaDirectorURLForTest(t *testing.T) *url.URL {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://relay.invalid/", nil)
	relay.multica.Director(req)
	return &url.URL{Scheme: req.URL.Scheme, Host: req.URL.Host}
}

func (relay *Relay) agentIdentityDirectorURLForTest(t *testing.T) *url.URL {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://relay.invalid/", nil)
	relay.agentIdentity.Director(req)
	return &url.URL{Scheme: req.URL.Scheme, Host: req.URL.Host}
}

func TestRelayRequestContextCancellationReleasesCapacity(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		<-req.Context().Done()
	})
	relay, signer, _ := testRelay(t, upstream, nil)
	token := mintTestToken(t, signer, "mdt_prepub-daemon", "")
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/daemon/runtimes/runtime-1/tasks/claim", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer mdt_prepub-daemon")
	req.Header.Set(protocol.SandboxRelayTokenHeader, token)
	done := make(chan struct{})
	go func() {
		relay.Middleware(http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled request did not return")
	}
}
