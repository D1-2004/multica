package agentmessagerouter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientIssuesBindingTokenWithServiceCredential(t *testing.T) {
	expiresAt := time.Date(2026, 7, 14, 10, 5, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/account-binding-tokens" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer service-credential" {
			t.Fatalf("Authorization = %q", got)
		}
		var body struct {
			AgentID     string `json:"agentId"`
			DispatchURL string `json:"dispatchUrl"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.AgentID != "agent-1" || body.DispatchURL != "https://multica.example.com/api/webhooks/agent-dispatch/v1_endpoint" {
			t.Fatalf("request body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"bindingToken": "bat_v1.secret-value",
			"expiresAt":    expiresAt.Format(time.RFC3339),
		})
	}))
	defer server.Close()

	client, err := NewClient(ClientConfig{
		BaseURL:           server.URL,
		ServiceCredential: "service-credential",
		HTTPClient:        server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	got, err := client.IssueBindingToken(context.Background(), "agent-1", "https://multica.example.com/api/webhooks/agent-dispatch/v1_endpoint")
	if err != nil {
		t.Fatalf("IssueBindingToken: %v", err)
	}
	if got.BindingToken != "bat_v1.secret-value" || !got.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("result = %#v", got)
	}
}

func TestClientCreatesDingTalkAccountSubscriptionOverHTTP(t *testing.T) {
	dispatchURL := "https://multica.example.com/api/webhooks/agent-dispatch/v1_endpoint"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/subscriptions" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer service-credential" {
			t.Fatalf("Authorization = %q", got)
		}
		var body struct {
			Source struct {
				Platform   string  `json:"platform"`
				TenantID   *string `json:"tenantId"`
				Type       string  `json:"type"`
				ExternalID string  `json:"externalId"`
			} `json:"source"`
			Agent struct {
				AgentID     string `json:"agentId"`
				DispatchURL string `json:"dispatchUrl"`
			} `json:"agent"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Source.Platform != "dingtalk" || body.Source.Type != "account" ||
			body.Source.ExternalID != "123" || body.Source.TenantID != nil ||
			body.Agent.AgentID != "agent-1" || body.Agent.DispatchURL != dispatchURL {
			t.Fatalf("request body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"code":    "success",
			"data": map[string]any{
				"sourceId":    "source-1",
				"agentId":     "agent-1",
				"dispatchUrl": dispatchURL,
				"status":      "active",
			},
		})
	}))
	defer server.Close()

	client, err := NewClient(ClientConfig{
		BaseURL:           server.URL,
		ServiceCredential: "service-credential",
		HTTPClient:        server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	subscription, err := client.CreateDingTalkAccountSubscription(
		context.Background(), "123", "agent-1", dispatchURL)
	if err != nil {
		t.Fatalf("CreateDingTalkAccountSubscription: %v", err)
	}
	if subscription.SourceID != "source-1" || subscription.AgentID != "agent-1" ||
		subscription.DispatchURL != dispatchURL || subscription.Status != "active" {
		t.Fatalf("subscription = %#v", subscription)
	}
}

func TestClientGetsAndDeletesSubscription(t *testing.T) {
	var deleteCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer service-credential" {
			t.Fatal("missing service credential")
		}
		if r.URL.Path != "/api/subscriptions/source-1" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"code":    "success",
				"data": map[string]any{
					"sourceId":    "source-1",
					"agentId":     "agent-1",
					"dispatchUrl": "https://multica.example.com/api/webhooks/agent-dispatch/v1_endpoint",
					"status":      "active",
				},
			})
		case http.MethodDelete:
			deleteCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"success": true,
				"code":    "success",
				"data": map[string]any{
					"sourceId":    "source-1",
					"agentId":     nil,
					"dispatchUrl": nil,
					"status":      "inactive",
				},
			})
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
	defer server.Close()

	client, err := NewClient(ClientConfig{
		BaseURL:           server.URL,
		ServiceCredential: "service-credential",
		HTTPClient:        server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	subscription, err := client.GetSubscription(context.Background(), "source-1")
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if subscription.SourceID != "source-1" || subscription.AgentID != "agent-1" || subscription.Status != "active" {
		t.Fatalf("subscription = %#v", subscription)
	}
	if err := client.DeleteSubscription(context.Background(), "source-1"); err != nil {
		t.Fatalf("DeleteSubscription should treat missing as success: %v", err)
	}
	if deleteCalls != 1 {
		t.Fatalf("delete calls = %d", deleteCalls)
	}
}

func TestClientMapsHTTP200SubscriptionNotFoundToSentinel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"code":    "business_error",
			"message": "subscription_not_found",
			"data":    nil,
		})
	}))
	defer server.Close()

	client := mustTestClient(t, server)
	_, err := client.GetSubscription(context.Background(), "source-1")
	if !errors.Is(err, ErrSubscriptionNotFound) {
		t.Fatalf("error = %v, want ErrSubscriptionNotFound", err)
	}
	var routerError *RouterAPIError
	if !errors.As(err, &routerError) || routerError.Code != "business_error" {
		t.Fatalf("error = %#v, want typed RouterAPIError", err)
	}
}

func TestClientPreservesTypedBindingTokenBusinessErrorWithoutLeakingMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":    "service_auth_not_configured",
			"message": "Bearer service-credential bat_v1.secret-value",
		})
	}))
	defer server.Close()

	client := mustTestClient(t, server)
	_, err := client.IssueBindingToken(context.Background(), "agent-1", "https://multica.example.com/dispatch")
	var routerError *RouterAPIError
	if !errors.As(err, &routerError) || routerError.Code != "service_auth_not_configured" {
		t.Fatalf("error = %#v, want service_auth_not_configured", err)
	}
	if containsAny(err.Error(), "service-credential", "bat_v1.secret-value") {
		t.Fatalf("unsafe error = %q", err)
	}
}

func TestClientRejectsInvalidSuccessfulSubscriptionPayloads(t *testing.T) {
	tests := []struct {
		name string
		data map[string]any
	}{
		{name: "different source", data: map[string]any{"sourceId": "source-2", "agentId": "agent-1", "dispatchUrl": "https://multica.example.com/api/webhooks/agent-dispatch/v1_endpoint", "status": "active"}},
		{name: "inactive", data: map[string]any{"sourceId": "source-1", "agentId": "agent-1", "dispatchUrl": "https://multica.example.com/api/webhooks/agent-dispatch/v1_endpoint", "status": "inactive"}},
		{name: "missing agent", data: map[string]any{"sourceId": "source-1", "dispatchUrl": "https://multica.example.com/api/webhooks/agent-dispatch/v1_endpoint", "status": "active"}},
		{name: "missing dispatch url", data: map[string]any{"sourceId": "source-1", "agentId": "agent-1", "status": "active"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "code": "success", "data": tt.data})
			}))
			defer server.Close()

			client := mustTestClient(t, server)
			if _, err := client.GetSubscription(context.Background(), "source-1"); err == nil {
				t.Fatal("expected invalid subscription response")
			}
		})
	}
}

func TestClientDeleteRequiresSuccessfulInactiveEnvelope(t *testing.T) {
	tests := []struct {
		name     string
		response map[string]any
		wantAPI  bool
	}{
		{
			name: "http 200 business error",
			response: map[string]any{
				"success": false,
				"code":    "internal_error",
				"message": "Bearer service-credential bat_v1.secret-value",
				"data":    nil,
			},
			wantAPI: true,
		},
		{
			name: "different source",
			response: map[string]any{
				"success": true,
				"code":    "success",
				"data": map[string]any{"sourceId": "source-2", "agentId": nil, "dispatchUrl": nil, "status": "inactive"},
			},
		},
		{
			name: "still active",
			response: map[string]any{
				"success": true,
				"code":    "success",
				"data": map[string]any{"sourceId": "source-1", "agentId": "agent-1", "dispatchUrl": "https://multica.example.com/dispatch", "status": "active"},
			},
		},
		{
			name: "partially null target",
			response: map[string]any{
				"success": true,
				"code":    "success",
				"data": map[string]any{"sourceId": "source-1", "agentId": "agent-1", "dispatchUrl": nil, "status": "inactive"},
			},
		},
		{
			name: "blank target values",
			response: map[string]any{
				"success": true,
				"code":    "success",
				"data": map[string]any{"sourceId": "source-1", "agentId": " ", "dispatchUrl": "\t", "status": "inactive"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(tt.response)
			}))
			defer server.Close()

			client := mustTestClient(t, server)
			err := client.DeleteSubscription(context.Background(), "source-1")
			if err == nil {
				t.Fatal("expected delete response rejection")
			}
			if containsAny(err.Error(), "service-credential", "bat_v1.secret-value") {
				t.Fatalf("unsafe error = %q", err)
			}
			if tt.wantAPI {
				var routerError *RouterAPIError
				if !errors.As(err, &routerError) || routerError.Code != "internal_error" {
					t.Fatalf("error = %#v, want internal_error", err)
				}
			}
		})
	}
}

func TestClientRejectsTrailingAndOversizedRouterResponses(t *testing.T) {
	valid := `{"bindingToken":"bat_v1.secret-value","expiresAt":"2026-07-14T10:05:00Z"}`
	tests := []struct {
		name string
		body string
	}{
		{name: "trailing json", body: valid + ` {"second":true}`},
		{name: "oversized trailing whitespace", body: valid + strings.Repeat(" ", maxRouterResponseBytes+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := mustTestClient(t, server)
			if _, err := client.IssueBindingToken(context.Background(), "agent-1", "https://multica.example.com/dispatch"); err == nil {
				t.Fatal("expected malformed router response")
			}
		})
	}
}

func TestClientRejectsInvalidOrFailedResponsesWithoutLeakingCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"Bearer service-credential bat_v1.secret-value"}`))
	}))
	defer server.Close()

	client, err := NewClient(ClientConfig{
		BaseURL:           server.URL,
		ServiceCredential: "service-credential",
		HTTPClient:        server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.IssueBindingToken(context.Background(), "agent-1", "https://multica.example.com/api/webhooks/agent-dispatch/v1_endpoint")
	if err == nil {
		t.Fatal("expected upstream error")
	}
	if got := err.Error(); got == "" || containsAny(got, "service-credential", "bat_v1.secret-value") {
		t.Fatalf("unsafe error = %q", got)
	}
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if len(candidate) > 0 && len(value) >= len(candidate) {
			for i := 0; i+len(candidate) <= len(value); i++ {
				if value[i:i+len(candidate)] == candidate {
					return true
				}
			}
		}
	}
	return false
}

func mustTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClient(ClientConfig{
		BaseURL:           server.URL,
		ServiceCredential: "service-credential",
		HTTPClient:        server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}
