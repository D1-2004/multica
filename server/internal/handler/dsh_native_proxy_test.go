package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestDSHNativeProxyPageCookiesAndOrigin(t *testing.T) {
	prefix := dshNativeProxyRoot + "00000000-0000-4000-8000-000000000001/"
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_multica/open" || r.Header.Get("Origin") != upstream.URL || r.Header.Get("X-Workspace-ID") != "" ||
			r.Header.Get("Cookie") != dshNativeGatewayCookie+"=native-session" {
			t.Error("native upstream leaked workbench credentials or lost path/origin", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment")
		w.Header().Set("Content-Security-Policy", "script-src 'unsafe-inline'")
		http.SetCookie(w, &http.Cookie{Name: dshNativeGatewayCookie, Value: "new-session", Path: "/", Secure: true, HttpOnly: true})
		_, _ = io.WriteString(w, `<!doctype html><html><head><script type="module" src="/assets/main.js"></script><script type="importmap">{"imports":{"plugin":"/modules/plugin.js"}}</script></head><body></body></html>`)
	}))
	defer upstream.Close()
	r := httptest.NewRequest("GET", "https://pre.test"+prefix+"_multica/open", nil)
	r.Header.Set("Cookie", "workbench-secret=must-not-forward; "+dshNativeProxyCookie+"=native-session")
	r.Header.Set("X-Workspace-ID", "must-not-forward")
	w := httptest.NewRecorder()
	w.Header().Set("Content-Security-Policy", "script-src 'self'")
	serveDSHNativeProxy(w, r, upstream.URL, "https://pre.test", prefix)
	if w.Code != 200 || w.Header().Get("Content-Disposition") != "" {
		t.Fatal("native HTML still downloads", w.Code)
	}
	for _, expected := range []string{prefix + "assets/main.js", prefix + "modules/plugin.js", "globalThis.fetch=", "'WebSocket'"} {
		if !strings.Contains(w.Body.String(), expected) {
			t.Fatal("native page lost a path/transport mapping", expected)
		}
	}
	policies := w.Header().Values("Content-Security-Policy")
	if len(policies) != 1 || strings.Contains(policies[0], "script-src 'unsafe-inline'") {
		t.Fatal("native HTML retained conflicting or unrestricted script policies")
	}
	nonce := regexp.MustCompile(`'nonce-([^']+)'`).FindStringSubmatch(policies[0])
	if !strings.Contains(policies[0], "script-src 'self' 'unsafe-eval' 'nonce-") || len(nonce) != 2 || strings.Count(w.Body.String(), `<script nonce="`+nonce[1]+`"`) != 3 {
		t.Fatal("bootstrap, module and import map scripts must share the response nonce")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != dshNativeProxyCookie || cookies[0].Path != prefix || !cookies[0].Secure || !cookies[0].HttpOnly {
		t.Fatal("native cookie escaped its access path")
	}
	for _, origin := range []string{"", "https://evil.test"} {
		r = httptest.NewRequest("POST", "https://pre.test"+prefix+"api/session/prompt", strings.NewReader("{}"))
		r.Header.Set("Origin", origin)
		w = httptest.NewRecorder()
		serveDSHNativeProxy(w, r, upstream.URL, "https://pre.test", prefix)
		if w.Code != http.StatusForbidden {
			t.Fatal("untrusted request reached native gateway")
		}
	}
}

func TestDSHNativeProxyWebSocket(t *testing.T) {
	prefix := dshNativeProxyRoot + "00000000-0000-4000-8000-000000000001/"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/remote.mux" || r.Header.Get("Cookie") != dshNativeGatewayCookie+"=native-session" {
			t.Error("incorrect native websocket scope")
		}
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		kind, payload, err := conn.ReadMessage()
		if err == nil {
			_ = conn.WriteMessage(kind, payload)
		}
	}))
	defer upstream.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveDSHNativeProxy(w, r, upstream.URL, "https://pre.test", prefix)
	}))
	defer proxy.Close()
	headers := http.Header{"Origin": {"https://pre.test"}, "Cookie": {dshNativeProxyCookie + "=native-session"}}
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(proxy.URL, "http")+prefix+"api/remote.mux", headers)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte("native-stream")); err != nil {
		t.Fatal(err)
	}
	kind, payload, err := conn.ReadMessage()
	if err != nil || kind != websocket.BinaryMessage || string(payload) != "native-stream" {
		t.Fatal("native stream did not survive path proxy", err)
	}
}
