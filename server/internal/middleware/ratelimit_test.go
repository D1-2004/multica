package middleware

import (
	"encoding/json"
	"github.com/multica-ai/multica/server/internal/forwarding"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func TestRateLimit_NilRedis(t *testing.T) {
	mw := RateLimit(nil, 5, time.Minute, nil)
	handler := mw(okHandler)

	req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req.RemoteAddr = "1.2.3.4:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("nil redis should pass through; got status %d", rec.Code)
	}
}

func TestRateLimit_AllowsUnderLimit(t *testing.T) {
	rdb := newRedisTestClient(t)
	mw := RateLimit(rdb, 3, time.Minute, nil)
	handler := mw(okHandler)

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
		req.RemoteAddr = "10.0.0.1:9000"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, rec.Code)
		}
	}
}

func TestRateLimit_BlocksOverLimit(t *testing.T) {
	rdb := newRedisTestClient(t)
	mw := RateLimit(rdb, 2, time.Minute, nil)
	handler := mw(okHandler)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
		req.RemoteAddr = "10.0.0.2:9000"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i+1, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req.RemoteAddr = "10.0.0.2:9000"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "too many requests" {
		t.Fatalf("unexpected error message: %q", body["error"])
	}
}

func TestRateLimit_RetryAfterHeader(t *testing.T) {
	rdb := newRedisTestClient(t)
	mw := RateLimit(rdb, 1, 2*time.Minute, nil)
	handler := mw(okHandler)

	req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req.RemoteAddr = "10.0.0.3:9000"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	req = httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req.RemoteAddr = "10.0.0.3:9000"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
	retryAfter := rec.Header().Get("Retry-After")
	if retryAfter != "120" {
		t.Fatalf("expected Retry-After=120, got %q", retryAfter)
	}
}

func TestRateLimit_DifferentIPs(t *testing.T) {
	rdb := newRedisTestClient(t)
	mw := RateLimit(rdb, 1, time.Minute, nil)
	handler := mw(okHandler)

	ips := []string{"10.0.1.1:9000", "10.0.1.2:9000", "10.0.1.3:9000"}
	for _, addr := range ips {
		req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
		req.RemoteAddr = addr
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("ip %s: expected 200, got %d", addr, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req.RemoteAddr = "10.0.1.1:9000"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for repeated IP, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req.RemoteAddr = "10.0.1.3:9000"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for repeated IP 10.0.1.3, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// X-Forwarded-For trust model tests
// ---------------------------------------------------------------------------

func mustParseCIDR(cidr string) *net.IPNet {
	_, n, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	return n
}

func TestExtractIP_NoTrustedProxies_IgnoresXFF(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req.RemoteAddr = "203.0.113.1:9000"
	req.Header.Set("X-Forwarded-For", "198.51.100.1, 203.0.113.1")

	ip := extractIP(req, nil)
	if ip != "203.0.113.1" {
		t.Fatalf("with no trusted proxies, expected RemoteAddr; got %q", ip)
	}
}

func TestExtractIP_TrustedProxy_HonorsXFF(t *testing.T) {
	trusted := []*net.IPNet{mustParseCIDR("10.0.0.0/8")}

	req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req.RemoteAddr = "10.0.0.1:9000"
	req.Header.Set("X-Forwarded-For", "198.51.100.42, 10.0.0.5")

	ip := extractIP(req, trusted)
	// Rightmost non-trusted: 198.51.100.42 (10.0.0.5 is trusted)
	if ip != "198.51.100.42" {
		t.Fatalf("expected rightmost non-trusted IP; got %q", ip)
	}
}

func TestExtractIP_UntrustedSource_IgnoresXFF(t *testing.T) {
	trusted := []*net.IPNet{mustParseCIDR("10.0.0.0/8")}

	req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req.RemoteAddr = "203.0.113.99:9000" // Not in trusted CIDR
	req.Header.Set("X-Forwarded-For", "198.51.100.1")

	ip := extractIP(req, trusted)
	if ip != "203.0.113.99" {
		t.Fatalf("untrusted source should use RemoteAddr; got %q", ip)
	}
}

func TestExtractIP_IPv6Normalization(t *testing.T) {
	req1 := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req1.RemoteAddr = "[::1]:9000"

	req2 := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req2.RemoteAddr = "[0:0:0:0:0:0:0:1]:9000"

	ip1 := extractIP(req1, nil)
	ip2 := extractIP(req2, nil)
	if ip1 != ip2 {
		t.Fatalf("IPv6 representations should normalize to same key: %q vs %q", ip1, ip2)
	}
}

func TestRateLimit_LuaScript_SetsTTL(t *testing.T) {
	rdb := newRedisTestClient(t)
	mw := RateLimit(rdb, 10, 30*time.Second, nil)
	handler := mw(okHandler)

	req := httptest.NewRequest(http.MethodPost, "/auth/send-code", nil)
	req.RemoteAddr = "10.0.99.1:9000"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	key := rateLimitKey("/auth/send-code", "10.0.99.1")
	ttl, err := rdb.TTL(req.Context(), key).Result()
	if err != nil {
		t.Fatalf("TTL: %v", err)
	}
	if ttl <= 0 || ttl > 31*time.Second {
		t.Fatalf("expected TTL ~30s, got %v", ttl)
	}
}

func TestParseTrustedProxies_Empty(t *testing.T) {
	if nets := ParseTrustedProxies(""); nets != nil {
		t.Fatalf("empty string should return nil, got %v", nets)
	}
	if nets := ParseTrustedProxies("  "); nets != nil {
		t.Fatalf("whitespace should return nil, got %v", nets)
	}
}

func TestParseTrustedProxies_Valid(t *testing.T) {
	nets := ParseTrustedProxies("10.0.0.0/8, 172.16.0.0/12")
	if len(nets) != 2 {
		t.Fatalf("expected 2 CIDRs, got %d", len(nets))
	}
}

func TestParseTrustedProxies_InvalidSkipped(t *testing.T) {
	nets := ParseTrustedProxies("10.0.0.0/8, not-a-cidr, 172.16.0.0/12")
	if len(nets) != 2 {
		t.Fatalf("expected 2 valid CIDRs (invalid skipped), got %d", len(nets))
	}
}

func TestForwardedLoginRateLimitKeepsSeparateClientBuckets(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	ips := make(chan string, 2)
	target := httptest.NewTLSServer(forwarding.AcceptClientIP(secret, "pre")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ips <- extractIP(r, nil)
		w.WriteHeader(http.StatusNoContent)
	})))
	defer target.Close()
	trusted := ParseTrustedProxies("127.0.0.1/32")
	gateway, err := forwarding.New(forwarding.Config{Targets: map[string]string{"pre": target.URL}, Transport: target.Client().Transport, RegistrationSecret: secret, ClientIP: func(r *http.Request) string { return RateLimitClientIP(r, trusted) }})
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, ip := range []string{"192.0.2.1", "192.0.2.2"} {
		r := httptest.NewRequest("POST", "/forward/pre/auth/fde/dingtalk", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("X-Forwarded-For", "8.8.8.8, "+ip)
		w := httptest.NewRecorder()
		gateway.Middleware(http.NotFoundHandler()).ServeHTTP(w, r)
		if w.Code != 204 {
			t.Fatalf("status=%d", w.Code)
		}
		select {
		case got := <-ips:
			if got != ip {
				t.Fatalf("client=%s want=%s", got, ip)
			}
			keys = append(keys, rateLimitKey("/auth/fde/dingtalk", got))
		case <-time.After(time.Second):
			t.Fatal("no target request")
		}
	}
	if keys[0] == keys[1] {
		t.Fatal("forwarded users share one login bucket")
	}
	direct := httptest.NewRequest("POST", "/auth/fde/dingtalk", nil)
	direct.RemoteAddr = "192.0.2.3:1234"
	direct.Header.Set("X-Forwarded-For", "8.8.8.8")
	if got := RateLimitClientIP(direct, trusted); got != "192.0.2.3" {
		t.Fatalf("untrusted XFF accepted: %s", got)
	}
}
