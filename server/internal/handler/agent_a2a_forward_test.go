package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const forwardTestTargetToken = "mca2a_0123456789abcdef0123456789abcdef01234567"

func TestNormalizeAgentA2AForwardTarget(t *testing.T) {
	allowed := []string{"https://pre-fde-workbench.dingtalk.com", "http://insecure.example", "https://x.example/path"}
	valid := "https://pre-fde-workbench.dingtalk.com/api/a2a/agents/99c83573-1392-4265-87e1-9c10da75f8e4/v1"
	got, err := normalizeAgentA2AForwardTarget(valid, allowed, "https://fde-workbench.dingtalk.com", "99c83573-1392-4265-87e1-9c10da75f8e4")
	if err != nil || got != valid {
		t.Fatalf("valid target = %q, %v", got, err)
	}

	cases := map[string]string{
		"origin not allowed": "https://evil.example/api/a2a/agents/99c83573-1392-4265-87e1-9c10da75f8e4/v1",
		"plain http":         "http://pre-fde-workbench.dingtalk.com/api/a2a/agents/99c83573-1392-4265-87e1-9c10da75f8e4/v1",
		"query string":       valid + "?x=1",
		"credentials":        "https://user:pass@pre-fde-workbench.dingtalk.com/api/a2a/agents/99c83573-1392-4265-87e1-9c10da75f8e4/v1",
		"not an rpc path":    "https://pre-fde-workbench.dingtalk.com/api/agents/99c83573/v1",
		"card path":          "https://pre-fde-workbench.dingtalk.com/api/a2a/agents/99c83573-1392-4265-87e1-9c10da75f8e4/.well-known/agent-card.json",
	}
	for name, target := range cases {
		if _, err := normalizeAgentA2AForwardTarget(target, allowed, "https://fde-workbench.dingtalk.com", "own-agent-id-000000"); err == nil {
			t.Errorf("%s: %q was accepted", name, target)
		}
	}

	if _, err := normalizeAgentA2AForwardTarget(valid, nil, "https://fde-workbench.dingtalk.com", ""); err == nil {
		t.Fatal("forwarding was accepted with an empty origin allow-list")
	}
	// Pointing an Agent at itself on the same deployment would loop.
	if _, err := normalizeAgentA2AForwardTarget(valid, allowed, "https://pre-fde-workbench.dingtalk.com", "99c83573-1392-4265-87e1-9c10da75f8e4"); err == nil {
		t.Fatal("self-forward was accepted")
	}
	// Another Agent on the same deployment is a legitimate test target.
	if _, err := normalizeAgentA2AForwardTarget(valid, allowed, "https://pre-fde-workbench.dingtalk.com", "another-agent-000000"); err != nil {
		t.Fatalf("same-origin different Agent rejected: %v", err)
	}
	if origins := normalizedAgentA2AForwardOrigins(allowed); len(origins) != 1 || origins[0] != "https://pre-fde-workbench.dingtalk.com" {
		t.Fatalf("normalized origins = %v", origins)
	}
}

func TestForwardAgentA2ARPCReplacesCredentialAndStreams(t *testing.T) {
	var received *http.Request
	var receivedBody string
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Clone(r.Context())
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Set-Cookie", "session=target")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"kind\":\"task\"}\n\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "data: {\"final\":true}\n\n")
		flusher.Flush()
	}))
	defer target.Close()

	h := &Handler{a2aForwardTransport: target.Client().Transport}
	body := `{"jsonrpc":"2.0","id":"1","method":"SendStreamingMessage","params":{}}`
	request := httptest.NewRequest(http.MethodPost, "/api/a2a/agents/prod-agent-000000000/v1", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer mca2a_prodprodprodprodprodprodprodprodprodprod")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("A2A-Version", "1.0")
	request.Header.Set("A2A-Extensions", "https://api-deap.dingtalk.com/a2a/extensions/dingtalk-event/v1")
	request.Header.Set("X-DingTalk-User-Id", "103262")
	request.Header.Set("X-DWS-Token", "deap-employee-token")
	request.Header.Set("Cookie", "multica_session=prod")
	request.Header.Set("X-Forwarded-For", "10.0.0.1")
	request.Header.Set("X-User-Id", "spoofed")
	request.Header.Set("X-Multica-Sandbox-Relay-Token", "relay")
	recorder := httptest.NewRecorder()

	h.forwardAgentA2ARPC(recorder, request, []byte(body), agentA2AForwardTarget{
		RPCURL: target.URL + "/api/a2a/agents/pre-agent-0000000000/v1",
		Token:  forwardTestTargetToken,
	}, "prod-agent-000000000")

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if received == nil {
		t.Fatal("target received nothing")
	}
	if got := received.Header.Get("Authorization"); got != "Bearer "+forwardTestTargetToken {
		t.Fatalf("Authorization = %q, want the target key", got)
	}
	if got := received.Header.Get(agentA2AForwardedHeader); got != "prod-agent-000000000" {
		t.Fatalf("loop marker = %q", got)
	}
	for _, kept := range []string{"A2A-Version", "A2A-Extensions", "X-DingTalk-User-Id", "X-DWS-Token", "Content-Type"} {
		if received.Header.Get(kept) == "" {
			t.Errorf("header %s was not forwarded", kept)
		}
	}
	for _, dropped := range []string{"Cookie", "X-Forwarded-For", "X-User-Id", "X-Multica-Sandbox-Relay-Token"} {
		if received.Header.Get(dropped) != "" {
			t.Errorf("header %s leaked to the target: %q", dropped, received.Header.Get(dropped))
		}
	}
	if receivedBody != body {
		t.Fatalf("target body = %q", receivedBody)
	}
	if got := recorder.Body.String(); !strings.Contains(got, `"kind":"task"`) || !strings.Contains(got, `"final":true`) {
		t.Fatalf("relayed stream = %q", got)
	}
	if recorder.Header().Get("Set-Cookie") != "" {
		t.Fatal("target Set-Cookie was relayed to the caller")
	}
	if recorder.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type = %q", recorder.Header().Get("Content-Type"))
	}
}

func TestAgentA2ARPCMethod(t *testing.T) {
	cases := map[string]string{
		`{"jsonrpc":"2.0","id":"1","method":"SendStreamingMessage"}`: "SendStreamingMessage",
		`  {"method":" GetTask "}`:                                   "GetTask",
		`[{"method":"GetTask"}]`:                                     "",
		`{"method":`:                                                 "",
		``:                                                           "",
	}
	for body, want := range cases {
		if got := agentA2ARPCMethod([]byte(body)); got != want {
			t.Errorf("agentA2ARPCMethod(%q) = %q, want %q", body, got, want)
		}
	}
}

func TestForwardAgentA2ARPCUnavailableTarget(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	transport := target.Client().Transport
	url := target.URL
	target.Close()

	h := &Handler{a2aForwardTransport: transport}
	request := httptest.NewRequest(http.MethodPost, "/api/a2a/agents/prod-agent-000000000/v1", strings.NewReader("{}"))
	recorder := httptest.NewRecorder()
	h.forwardAgentA2ARPC(recorder, request, []byte("{}"), agentA2AForwardTarget{
		RPCURL: url + "/api/a2a/agents/pre-agent-0000000000/v1",
		Token:  forwardTestTargetToken,
	}, "prod-agent-000000000")
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", recorder.Code)
	}
}
