package forwarding

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testGateway(t *testing.T, handler http.HandlerFunc) (*Gateway, *httptest.Server) {
	t.Helper()
	upstream := httptest.NewTLSServer(handler)
	t.Cleanup(upstream.Close)
	g, err := New(map[string]string{"pre": upstream.URL}, upstream.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	return g, upstream
}

func TestGatewaySessionIsolationAndBody(t *testing.T) {
	g, _ := testGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/context-capabilities/links/redeem" || r.URL.RawQuery != "tab=scope" {
			t.Errorf("bad target URL: %s", r.URL)
		}
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"token":"link"}` {
			t.Error("body changed")
		}
		if r.Header.Get("Cookie") != "multica_auth=pre-token; multica_csrf=pre-csrf" {
			t.Errorf("cookie leak: %s", r.Header.Get("Cookie"))
		}
		for _, h := range []string{"Authorization", "X-User-ID", "X-Workspace-ID", "X-Forwarded-Host", "X-Smuggled"} {
			if r.Header.Get(h) != "" {
				t.Errorf("forwarded %s", h)
			}
		}
		if r.Header.Get("X-CSRF-Token") != "pre-csrf" {
			t.Error("missing csrf")
		}
		http.SetCookie(w, &http.Cookie{Name: "multica_auth", Value: "new", Path: "/", Domain: "pre.example.test", HttpOnly: true, Secure: true})
		http.SetCookie(w, &http.Cookie{Name: "multica_csrf", Value: "csrf", Path: "/", Secure: true})
		w.WriteHeader(201)
		_, _ = w.Write([]byte("saved"))
	})
	req := httptest.NewRequest("POST", "https://prod.example.test/forward/pre/api/context-capabilities/links/redeem?tab=scope", strings.NewReader(`{"token":"link"}`))
	req.Header.Set("Cookie", "multica_auth=prod; mf_other_multica_auth=other; mf_pre_multica_auth=pre-token; mf_pre_multica_csrf=pre-csrf")
	req.Header.Set("Authorization", "Bearer prod")
	req.Header.Set("X-User-ID", "admin")
	req.Header.Set("X-Workspace-ID", "prod-ws")
	req.Header.Set("X-Forwarded-Host", "evil.test")
	req.Header.Set("X-CSRF-Token", "pre-csrf")
	req.Header.Set("Connection", "X-Smuggled")
	req.Header.Set("X-Smuggled", "bad")
	rec := httptest.NewRecorder()
	g.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("fell through") })).ServeHTTP(rec, req)
	if rec.Code != 201 || rec.Body.String() != "saved" {
		t.Fatalf("response: %d %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("cookies: %v", cookies)
	}
	for _, c := range cookies {
		if !strings.HasPrefix(c.Name, "mf_pre_") || c.Path != "/forward/pre/" || c.Domain != "" {
			t.Fatalf("bad cookie: %s", c)
		}
	}
}

func TestGatewayRejectsUnknownRoutesAndNeverFallsBack(t *testing.T) {
	calls := 0
	g, _ := testGateway(t, func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(503) })
	for _, p := range []string{"/forward/unknown/dingtalk/configure", "/forward/pre/api/internal/logs/tail", "/forward/pre/api/context-capabilities/admin", "/forward/pre/api/context-capabilities/../config", "/forward/pre/api%2fconfig", "/forward/pre//api/config", "/forward/pre/forward/pre/api/config"} {
		w := httptest.NewRecorder()
		g.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("fallback") })).ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code < 400 {
			t.Errorf("accepted %s: %d", p, w.Code)
		}
	}
	if calls != 0 {
		t.Fatalf("upstream calls: %d", calls)
	}
	w := httptest.NewRecorder()
	g.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("fallback") })).ServeHTTP(w, httptest.NewRequest("GET", "/forward/pre/api/config", nil))
	if w.Code != 503 || calls != 1 {
		t.Fatalf("failure behavior: %d %d", w.Code, calls)
	}
}

func TestGatewayRedirectAndCallbackCookies(t *testing.T) {
	var home string
	g, up := testGateway(t, func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "multica_mcpc_abc", Value: "nonce", Path: "/api/connectors/oauth/callback", HttpOnly: true, Secure: true})
		http.Redirect(w, r, home+"/dingtalk/configure?connected=github", 302)
	})
	home = up.URL
	w := httptest.NewRecorder()
	g.Middleware(http.NotFoundHandler()).ServeHTTP(w, httptest.NewRequest("POST", "https://prod.test/forward/pre/api/context-capabilities/agents/a/connections/start", nil))
	if got := w.Header().Get("Location"); got != "/forward/pre/dingtalk/configure?connected=github" {
		t.Fatal(got)
	}
	c := w.Result().Cookies()[0]
	if c.Name != "multica_mcpc_abc" || c.Path != "/api/connectors/oauth/callback" {
		t.Fatal(c)
	}
}

func TestGatewayPreventsLoopsAndNetworkFallback(t *testing.T) {
	g, up := testGateway(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	r := httptest.NewRequest("GET", "/forward/pre/api/config", nil)
	r.Header.Set(HopHeader, "pre")
	w := httptest.NewRecorder()
	g.Middleware(http.NotFoundHandler()).ServeHTTP(w, r)
	if w.Code != 508 {
		t.Fatalf("loop: %d", w.Code)
	}
	up.Close()
	w = httptest.NewRecorder()
	g.Middleware(http.NotFoundHandler()).ServeHTTP(w, httptest.NewRequest("GET", "/forward/pre/api/config", nil))
	if w.Code != 502 {
		t.Fatalf("network: %d", w.Code)
	}
}

func TestConfigurationRejectsUnsafeTargets(t *testing.T) {
	for _, origin := range []string{"http://pre.test", "https://user:pass@pre.test", "https://pre.test/path", "https://pre.test?secret=x", "https://pre.test#x", "https://pre.test:bad"} {
		if _, err := New(map[string]string{"pre": origin}, nil); err == nil {
			t.Errorf("accepted %q", origin)
		}
	}
	if _, err := New(map[string]string{"../pre": "https://pre.test"}, nil); err == nil {
		t.Error("accepted unsafe key")
	}
	for _, base := range []string{"https://prod.test/forward/pre", "https://prod.test/forward/candidate-1"} {
		if _, _, err := ParsePublicBase(base); err != nil {
			t.Fatal(err)
		}
	}
	for _, base := range []string{"https://prod.test", "https://prod.test/forward/pre/api", "https://prod.test/forward/../pre", "http://prod.test/forward/pre", "https://prod.test/forward/pre?x=1"} {
		if _, _, err := ParsePublicBase(base); err == nil {
			t.Errorf("accepted public base %s", base)
		}
	}
}

func TestCallbackOnlyCarriesStateBoundCookie(t *testing.T) {
	g, _ := testGateway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "multica_mcpc_abc=nonce" {
			t.Errorf("callback cookie leak: %s", r.Header.Get("Cookie"))
		}
		if r.URL.RawQuery != "code=code&state=state" {
			t.Error("callback query changed")
		}
		http.SetCookie(w, &http.Cookie{Name: "multica_mcpc_abc", Path: "/api/connectors/oauth/callback", MaxAge: -1})
		http.Redirect(w, r, "https://prod.test/forward/pre/dingtalk/configure?connected=github", 302)
	})
	r := httptest.NewRequest("GET", "https://prod.test/api/connectors/oauth/callback?code=code&state=state", nil)
	r.Header.Set("Cookie", "multica_auth=prod; multica_mcpc_other=other; multica_mcpc_abc=nonce")
	w := httptest.NewRecorder()
	g.Callback(w, r, "pre", "/api/connectors/oauth/callback", "multica_mcpc_abc")
	if w.Code != 302 || w.Header().Get("Location") != "https://prod.test/forward/pre/dingtalk/configure?connected=github" {
		t.Fatalf("result: %d %s", w.Code, w.Header().Get("Location"))
	}
	if c := w.Result().Cookies(); len(c) != 1 || c[0].MaxAge != -1 || c[0].Path != "/api/connectors/oauth/callback" {
		t.Fatalf("callback cookie: %v", c)
	}
}

func TestLocalAssetsServeTargetBuildWithoutForwardingAgain(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_next/static/chunks/app.js" || r.Header.Get("Cookie") != "" {
			t.Errorf("local request %s %s", r.URL, r.Header.Get("Cookie"))
		}
		_, _ = w.Write([]byte("target-build"))
	}))
	defer local.Close()
	h, err := LocalAssets("pre", local.URL, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/forward/pre/_next/static/chunks/app.js", nil)
	r.Header.Set("Cookie", "multica_auth=prod")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "target-build" {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestAssetMountSurvivesDisablingPublicLinks(t *testing.T) {
	target, err := AssetTarget("/forward/pre", "")
	if err != nil || target != "pre" {
		t.Fatalf("disabled link broke asset mount: %q %v", target, err)
	}
	if _, err := AssetTarget("", "https://prod.test/forward/pre"); err == nil {
		t.Fatal("public links enabled without a matching build")
	}
	if _, err := AssetTarget("/forward/other", "https://prod.test/forward/pre"); err == nil {
		t.Fatal("mismatched build accepted")
	}
}
