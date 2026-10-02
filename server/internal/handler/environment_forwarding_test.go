package handler

import (
	"context"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/forwarding"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestConfigurationLinkUsesPublicForwardBase(t *testing.T) {
	h := &Handler{cfg: Config{AppURL: "https://pre-app.example.test", ForwardPublicBaseURL: "https://app.example.test/forward/pre"}}
	origin, err := h.contextConfigLinkOrigin()
	if err != nil || origin != "https://app.example.test/forward/pre" {
		t.Fatalf("origin=%q err=%v", origin, err)
	}
	h.cfg.ForwardPublicBaseURL = "https://app.example.test/arbitrary"
	if _, err = h.contextConfigLinkOrigin(); err == nil {
		t.Fatal("invalid base accepted")
	}
}

func TestConnectorForwardReturnIsLimitedToConfiguredPage(t *testing.T) {
	h := &Handler{cfg: Config{AppURL: "https://pre-app.example.test", ForwardPublicBaseURL: "https://app.example.test/forward/pre"}}
	scope := connectorOAuthScope{ScopeType: "person", AgentID: "agent"}
	want := "https://app.example.test/forward/pre/dingtalk/configure?agent=agent"
	got, err := h.connectorOAuthReturnTo(context.Background(), want, scope)
	if err != nil || got != want {
		t.Fatalf("return=%q err=%v", got, err)
	}
	for _, bad := range []string{"https://app.example.test/forward/other/dingtalk/configure", "https://app.example.test/settings", "https://app.example.test/forward/pre/dingtalk/configure/../settings"} {
		if _, err := h.connectorOAuthReturnTo(context.Background(), bad, scope); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	own := "https://pre-app.example.test/dingtalk/configure?agent=agent"
	if got, err := h.connectorOAuthReturnTo(context.Background(), own, scope); err != nil || got != own {
		t.Fatalf("direct pre flow changed: %s %v", got, err)
	}
}

// The production callback keeps its origin and executes the real pre-release
// handler over HTTP; registration is consumed exactly once on production.
func TestConnectorCallbackTransparentForwarding(t *testing.T) {
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
	prod.Forwarding, err = forwarding.New(map[string]string{"pre": upstream.URL}, upstream.Client().Transport)
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

func TestConnectorOAuthPublicOriginRoundTrip(t *testing.T) {
	f := newCatalogFixture(t)
	dcr := f.create(t, f.dcr)
	f.offer(t, dcr.ID)
	f.grant(t, contextcap.ScopePerson, catalogTestStaff)
	useConnectorOAuthForward(t, f.h)
	pre := *f.h
	target := httptest.NewTLSServer(catalogAPIRouter(&pre))
	defer target.Close()
	pre.cfg.AppURL = target.URL
	pre.cfg.FrontendOrigin = target.URL
	pre.cfg.ForwardPublicBaseURL = catalogAppOrigin + "/forward/pre"
	var err error
	f.h.Forwarding, err = forwarding.New(map[string]string{"pre": target.URL}, target.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	started, err := pre.startConnectorOAuth(context.Background(), connectorOAuthStart{
		connectorOAuthScope: f.scope(dcr.ID, contextcap.ScopePerson, catalogTestStaff),
		ReturnTo:            pre.cfg.ForwardPublicBaseURL + "/dingtalk/configure?agent=" + f.agentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.provideAuthorizeURL(t, started.AuthorizeURL)
	authorize, err := url.Parse(started.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	if got := authorize.Query().Get("redirect_uri"); got != catalogAppOrigin+connectorOAuthCallbackPath {
		t.Fatalf("callback %s", got)
	}
	req := httptest.NewRequest("GET", catalogAppOrigin+connectorOAuthCallbackPath+"?code=good-code&state="+url.QueryEscape(authorize.Query().Get("state")), nil)
	req.AddCookie(started.Cookie)
	result := httptest.NewRecorder()
	f.h.ConnectorOAuthCallback(result, req)
	if result.Code != 302 || !strings.HasPrefix(result.Header().Get("Location"), pre.cfg.ForwardPublicBaseURL+"/dingtalk/configure?") || !strings.Contains(result.Header().Get("Location"), "connected=") {
		t.Fatalf("result=%d %s %s", result.Code, result.Header().Get("Location"), result.Body.String())
	}
	if _, err := contextcap.GetCredential(context.Background(), testPool, f.personKey(dcr.ID)); err != nil {
		t.Fatalf("credential not stored: %v", err)
	}
}

func TestForwardedConfigurationLinksRemainRedacted(t *testing.T) {
	link := "https://prod.example.test/forward/pre/dingtalk/configure?link=secretBearer"
	for _, value := range []string{link, url.QueryEscape(link)} {
		if got := redactContextConfigLinks(value); got != "[configuration link]" {
			t.Fatalf("forwarded link not redacted: %q", got)
		}
	}
}

func TestInvalidProxiedOAuthKeepsPublicRecoveryLinks(t *testing.T) {
	h := &Handler{cfg: Config{AppURL: "https://pre-app.example.test", ForwardPublicBaseURL: "https://app.example.test/forward/pre"}}
	r := httptest.NewRequest("GET", connectorOAuthCallbackPath, nil)
	r.Header.Set(forwarding.HopHeader, "pre")
	w := httptest.NewRecorder()
	h.ConnectorOAuthCallback(w, r)
	if strings.Contains(w.Body.String(), "https://pre-app.example.test") || !strings.Contains(w.Body.String(), "https://app.example.test/forward/pre/dingtalk/configure") {
		t.Fatalf("recovery links leave public origin: %s", w.Body.String())
	}
}
