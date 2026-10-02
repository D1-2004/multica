package forwarding

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGatewayAuthenticatesClientIPWithoutTrustingBrowserHeaders(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	var clients []string
	target := httptest.NewTLSServer(AcceptClientIP(secret, "pre")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clients = append(clients, ClientIP(r.Context()))
		w.WriteHeader(204)
	})))
	defer target.Close()
	g, err := New(Config{Targets: map[string]string{"pre": target.URL}, Transport: target.Client().Transport, RegistrationSecret: secret, ClientIP: func(r *http.Request) string { ip, _, _ := net.SplitHostPort(r.RemoteAddr); return ip }})
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"192.0.2.1", "192.0.2.2"} {
		r := httptest.NewRequest("POST", "/forward/pre/auth/fde/dingtalk", nil)
		r.RemoteAddr = net.JoinHostPort(ip, "4567")
		r.Header.Set(clientIPHeader, "evil")
		r.Header.Set("X-Forwarded-For", "8.8.8.8")
		w := httptest.NewRecorder()
		g.Middleware(http.NotFoundHandler()).ServeHTTP(w, r)
		if w.Code != 204 {
			t.Fatalf("status=%d", w.Code)
		}
	}
	if len(clients) != 2 || clients[0] != "192.0.2.1" || clients[1] != "192.0.2.2" {
		t.Fatalf("lost client identities: %v", clients)
	}
}

func TestClientIPAssertionCannotChangeTargetPathMethodOrIP(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	for _, change := range []string{"ip", "path", "method", "target", "expired", "bad_signature"} {
		t.Run(change, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/auth/fde/dingtalk", nil)
			at := time.Now()
			if change == "expired" {
				at = at.Add(-2 * time.Minute)
			}
			signClientIP(r, secret, "pre", "192.0.2.1", at)
			switch change {
			case "ip":
				r.Header.Set(clientIPHeader, "192.0.2.2")
			case "path":
				r.URL.Path = "/auth/other"
			case "method":
				r.Method = "GET"
			case "target":
				r.Header.Set(HopHeader, "other")
			case "bad_signature":
				r.Header.Set(clientIPSignatureHeader, "bad")
			}
			w := httptest.NewRecorder()
			AcceptClientIP(secret, "pre")(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("forgery accepted") })).ServeHTTP(w, r)
			if w.Code != 401 {
				t.Fatalf("status=%d", w.Code)
			}
		})
	}
	r := httptest.NewRequest("GET", "/api/config", nil)
	w := httptest.NewRecorder()
	AcceptClientIP(secret, "pre")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ClientIP(r.Context()) != "" {
			t.Error("direct request acquired identity")
		}
		w.WriteHeader(204)
	})).ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
}
