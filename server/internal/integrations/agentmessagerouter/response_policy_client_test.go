package agentmessagerouter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestResponsePolicyCapabilityNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		data      any
		supported bool
	}{
		{"old_router", 404, nil, false},
		{"new_router", 200, map[string]any{"versions": []int{1}, "modes": []string{"legacy", "multica_coordinator"}}, true},
		{"unknown_version", 200, map[string]any{"versions": []int{2}, "modes": []string{"legacy", "multica_coordinator"}}, false},
		{"missing_rollback_mode", 200, map[string]any{"versions": []int{1}, "modes": []string{"multica_coordinator"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/subscriptions/response-policy-capabilities" || r.Header.Get("Authorization") != "Bearer test-service" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "code": "success", "data": tc.data})
			}))
			defer server.Close()
			client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "test-service"})
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.SupportsResponsePolicy(context.Background())
			if err != nil || got != tc.supported {
				t.Fatalf("supported=%v err=%v", got, err)
			}
		})
	}
}

func TestResponsePolicyUpdateHasIndependentContract(t *testing.T) {
	desired := protocol.DingTalkResponsePolicy{Version: 1, Mode: "multica_coordinator", Revision: 5, ShowAITag: false}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PATCH" || r.URL.Path != "/api/subscriptions/source-1/response-policy" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body) != 2 || string(body["agentId"]) != `"agent-1"` {
			t.Fatalf("unexpected body=%s", body)
		}
		var actual protocol.DingTalkResponsePolicy
		if err := json.Unmarshal(body["responsePolicy"], &actual); err != nil || actual != desired {
			t.Fatalf("policy=%+v err=%v", actual, err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "code": "success", "data": Subscription{
			SourceID: "source-1", AgentID: "agent-1", Status: "active", ResponsePolicy: &actual,
		}})
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "test-service"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateSubscriptionResponsePolicy(context.Background(), "source-1", "agent-1", desired); err != nil {
		t.Fatal(err)
	}
}

func TestResponseReceiptCallbackRejectsCredentialExfiltration(t *testing.T) {
	client, err := NewClient(ClientConfig{BaseURL: "https://router.internal/prefix", ServiceCredential: "test-service"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/dispatch-tasks/task-1/response-receipt"
	for _, raw := range []string{path, "https://ROUTER.internal:443/prefix" + path} {
		got, err := client.NormalizeResponseReceiptCallback(raw)
		if err != nil || got != path {
			t.Errorf("normalize %q=%q err=%v", raw, got, err)
		}
	}
	for _, raw := range []string{
		"https://evil.example/prefix" + path,
		"https://router.internal.evil.example/prefix" + path,
		"https://user:password@router.internal/prefix" + path,
		"http://router.internal/prefix" + path,
		"//router.internal/prefix" + path,
		"https://router.internal" + path,
		path + "?redirect=evil", path + "#fragment", path + "?",
		"/api/v1/dispatch-tasks/task-1%2Fother/response-receipt",
		"/api/v1/dispatch-tasks/task-1/../other/response-receipt",
		"/api/v1/dispatch-tasks/task-1/execution-result",
	} {
		if got, err := client.NormalizeResponseReceiptCallback(raw); err == nil {
			t.Errorf("unsafe callback accepted %q -> %q", raw, got)
		}
	}
}

func TestResponseReceiptRequiresMatchingDurableAcknowledgement(t *testing.T) {
	for _, matches := range []bool{true, false} {
		t.Run(map[bool]string{true: "matching", false: "wrong_action"}[matches], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/api/v1/dispatch-tasks/task-1/response-receipt" || r.Header.Get("Authorization") != "Bearer test-service" {
					t.Errorf("unexpected receipt request")
				}
				var receipt protocol.DingTalkResponseReceipt
				if err := json.NewDecoder(r.Body).Decode(&receipt); err != nil {
					t.Fatal(err)
				}
				action := receipt.ActionID
				if !matches {
					action = "other-action"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "code": "success", "data": map[string]string{"dispatchTaskId": "task-1", "actionId": action, "state": receipt.State}})
			}))
			defer server.Close()
			client, err := NewClient(ClientConfig{BaseURL: server.URL, ServiceCredential: "test-service"})
			if err != nil {
				t.Fatal(err)
			}
			err = client.SubmitResponseReceipt(context.Background(), server.URL+"/api/v1/dispatch-tasks/task-1/response-receipt", protocol.DingTalkResponseReceipt{
				RequestID: "request-1", AgentID: "agent-1", ActionID: "action-1", State: "delivered", OccurredAt: 1,
			})
			if (err == nil) != matches {
				t.Fatalf("matches=%v err=%v", matches, err)
			}
		})
	}
}

func TestResponsePolicyPinsDurableTargetAcrossConfigChanges(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	base := server.URL
	client, err := NewClient(ClientConfig{BaseURLProvider: func() string { return base }, ServiceCredential: "test-service"})
	if err != nil {
		t.Fatal(err)
	}
	base = server.URL + "/different-environment"
	if _, err := client.SupportsResponsePolicy(context.Background()); err == nil {
		t.Fatal("target change allowed a request for the previous durable target")
	}
	if err := client.SubmitResponseReceipt(context.Background(), "/api/v1/dispatch-tasks/task-1/response-receipt", protocol.DingTalkResponseReceipt{
		RequestID: "request-1", AgentID: "agent-1", ActionID: "action-1", State: "delivered", OccurredAt: 1,
	}); err == nil {
		t.Fatal("receipt crossed environment boundary")
	}
	if requests != 0 {
		t.Fatalf("made %d requests to a changed target", requests)
	}
}
