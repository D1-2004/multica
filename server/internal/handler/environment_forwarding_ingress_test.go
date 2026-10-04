package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/forwarding"
)

func TestIngressConnectorCallbackTransparentForwarding(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if r.URL.Path != connectorOAuthCallbackPath {
			t.Errorf("callback path %s", r.URL.Path)
		}
		c, err := r.Cookie(connectorOAuthCookieName(connectorOAuthForwardStateSHA256(r.URL.Query().Get("state"))))
		if err != nil || c.Value != "nonce" {
			t.Errorf("missing browser binding: %v", err)
		}
		if _, err := r.Cookie("multica_auth"); err == nil {
			t.Error("production login leaked")
		}
		http.Redirect(w, r, "https://prod.example.test/forward/pre/dingtalk/configure?connected=github", 302)
	}))
	defer upstream.Close()
	prod := &Handler{cfg: Config{AppURL: "https://prod.example.test", FrontendOrigin: "https://prod.example.test"}}
	useConnectorOAuthForward(t, prod)
	var err error
	prod.Forwarding, err = forwarding.New(forwarding.Config{Targets: map[string]string{"pre": upstream.URL}, Transport: upstream.Client().Transport})
	if err != nil {
		t.Fatal(err)
	}
	pre := &Handler{cfg: Config{A2AForwardRegistrationSecret: connectorOAuthForwardTestSecret}}
	state := forwardState(upstream.URL)
	reg := connectorOAuthForwardRegistration{ForwardTarget: "pre", HomeOrigin: upstream.URL, WorkspaceID: "11111111-1111-4111-8111-111111111111", AgentID: "22222222-2222-4222-8222-222222222222", ConnectorID: "33333333-3333-4333-8333-333333333333", ScopeType: "person", ExpiresAtMs: time.Now().Add(time.Minute).UnixMilli()}
	if err := pre.registerConnectorOAuthForward(context.Background(), "https://prod.example.test", state, reg); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "https://prod.example.test"+connectorOAuthCallbackPath+"?state="+url.QueryEscape(state)+"&code=provider-code", nil)
	req.AddCookie(&http.Cookie{Name: "multica_auth", Value: "prod-secret"})
	req.AddCookie(&http.Cookie{Name: connectorOAuthCookieName(connectorOAuthForwardStateSHA256(state)), Value: "nonce"})
	for attempt := 0; attempt < 2; attempt++ {
		rec := httptest.NewRecorder()
		prod.ConnectorOAuthCallback(rec, req)
		if attempt == 0 {
			if rec.Code != 302 || rec.Header().Get("Location") != "https://prod.example.test/forward/pre/dingtalk/configure?connected=github" {
				t.Fatalf("response=%d %s", rec.Code, rec.Body.String())
			}
		} else if rec.Code != 400 {
			t.Fatalf("replay=%d", rec.Code)
		}
	}
	if upstreamCalls != 1 {
		t.Fatalf("calls=%d", upstreamCalls)
	}
	reg.ForwardTarget = "unknown"
	if err := pre.registerConnectorOAuthForward(context.Background(), "https://prod.example.test", state, reg); err == nil {
		t.Fatal("unknown target registered")
	}
	reg.ForwardTarget = "pre"
	reg.HomeOrigin = "https://evil.example.test"
	if err := pre.registerConnectorOAuthForward(context.Background(), "https://prod.example.test", state, reg); err == nil {
		t.Fatal("target origin mismatch registered")
	}
}

func TestIngressInvalidProxiedOAuthKeepsPublicRecoveryLinks(t *testing.T) {
	h := &Handler{cfg: Config{AppURL: "https://pre-app.example.test", ForwardPublicBaseURL: "https://app.example.test/forward/pre"}}
	r := httptest.NewRequest("GET", connectorOAuthCallbackPath, nil)
	r.Header.Set(forwarding.HopHeader, "pre")
	w := httptest.NewRecorder()
	h.ConnectorOAuthCallback(w, r)
	if strings.Contains(w.Body.String(), "https://pre-app.example.test") || !strings.Contains(w.Body.String(), "https://app.example.test/forward/pre/dingtalk/configure") {
		t.Fatalf("recovery links leave public origin: %s", w.Body.String())
	}
}
