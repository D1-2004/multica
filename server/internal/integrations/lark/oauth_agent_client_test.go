package lark

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOAuthAgentClientResolvesUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/lark/oauth/user" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("x-internal-secret"); got != "shh" {
			t.Errorf("x-internal-secret = %q", got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["code"] != "authcode" || body["redirect_uri"] != "http://localhost:3000/auth/callback" {
			t.Errorf("unexpected body: %v", body)
		}
		_, _ = w.Write([]byte(`{"user":{"union_id":"on_abc","open_id":"ou_x","name":"张三","avatar_url":"https://p.example/a.png"}}`))
	}))
	t.Cleanup(srv.Close)

	client := NewOAuthAgentClient(OAuthAgentClientConfig{BaseURL: srv.URL, InternalSecret: "shh"})
	user, err := client.ResolveOAuthUser(context.Background(), "authcode", "http://localhost:3000/auth/callback")
	if err != nil {
		t.Fatalf("ResolveOAuthUser: %v", err)
	}
	if user.UnionID != "on_abc" || user.Name != "张三" {
		t.Errorf("unexpected user: %+v", user)
	}
}

func TestOAuthAgentClientRejectsMissingUnionID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"user":{"name":"张三"}}`))
	}))
	t.Cleanup(srv.Close)

	client := NewOAuthAgentClient(OAuthAgentClientConfig{BaseURL: srv.URL, InternalSecret: "shh"})
	_, err := client.ResolveOAuthUser(context.Background(), "authcode", "http://localhost:3000/auth/callback")
	if err == nil || !strings.Contains(err.Error(), "union_id") {
		t.Fatalf("expected union_id error, got: %v", err)
	}
}

func TestOAuthAgentClientSurfacesUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"ok":false,"error":"failed to resolve Feishu OAuth user"}`))
	}))
	t.Cleanup(srv.Close)

	client := NewOAuthAgentClient(OAuthAgentClientConfig{BaseURL: srv.URL, InternalSecret: "shh"})
	_, err := client.ResolveOAuthUser(context.Background(), "authcode", "http://localhost:3000/auth/callback")
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("expected HTTP 502 error, got: %v", err)
	}
}

func TestOAuthAgentClientIsConfigured(t *testing.T) {
	if NewOAuthAgentClient(OAuthAgentClientConfig{BaseURL: "http://x"}).IsConfigured() {
		t.Error("missing secret should not be configured")
	}
	if !NewOAuthAgentClient(OAuthAgentClientConfig{BaseURL: "http://x", InternalSecret: "s"}).IsConfigured() {
		t.Error("base+secret should be configured")
	}
}
