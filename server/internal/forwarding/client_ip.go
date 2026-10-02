package forwarding

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"time"
)

const (
	clientIPHeader          = "X-Multica-Forward-Client-IP"
	clientIPTimestampHeader = "X-Multica-Forward-Client-Timestamp"
	clientIPSignatureHeader = "X-Multica-Forward-Client-Signature"
)

type clientIPKey struct{}

// ClientIP is authenticated rate-limit metadata, never a user identity.
func ClientIP(ctx context.Context) string { ip, _ := ctx.Value(clientIPKey{}).(string); return ip }

func clientIPPayload(r *http.Request, target, ip string) []byte {
	return []byte("forward-client-ip:v1\n" + target + "\n" + r.Method + "\n" + r.URL.RequestURI() + "\n" + ip)
}

func signClientIP(r *http.Request, secret []byte, target, ip string, now time.Time) {
	parsed := net.ParseIP(ip)
	if len(secret) < 32 || parsed == nil {
		return
	}
	ip = parsed.String()
	timestamp := strconv.FormatInt(now.UnixMilli(), 10)
	r.Header.Set(HopHeader, target)
	r.Header.Set(clientIPHeader, ip)
	r.Header.Set(clientIPTimestampHeader, timestamp)
	r.Header.Set(clientIPSignatureHeader, SignRegistration(secret, timestamp, clientIPPayload(r, target, ip)))
}

// AcceptClientIP verifies the target, method, URI, address and short timestamp
// window before exposing an address to rate limiting. Replays retain the same
// IP bucket and gain no application authority. Raw XFF is never used here.
func AcceptClientIP(secret []byte, target string) func(http.Handler) http.Handler {
	key := append([]byte(nil), secret...)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			present := false
			for _, name := range []string{clientIPHeader, clientIPTimestampHeader, clientIPSignatureHeader} {
				if len(r.Header.Values(name)) != 0 {
					present = true
				}
			}
			if !present {
				next.ServeHTTP(w, r)
				return
			}
			for _, name := range []string{clientIPHeader, clientIPTimestampHeader, clientIPSignatureHeader, HopHeader} {
				if len(r.Header.Values(name)) != 1 {
					http.Error(w, "invalid forwarding client assertion", 401)
					return
				}
			}
			ip := r.Header.Get(clientIPHeader)
			if len(key) < 32 || !targetName.MatchString(target) || r.Header.Get(HopHeader) != target || net.ParseIP(ip) == nil ||
				!VerifyRegistration(key, r.Header.Get(clientIPTimestampHeader), r.Header.Get(clientIPSignatureHeader), clientIPPayload(r, target, ip), time.Now(), time.Minute) {
				http.Error(w, "invalid forwarding client assertion", 401)
				return
			}
			for _, name := range []string{clientIPHeader, clientIPTimestampHeader, clientIPSignatureHeader} {
				r.Header.Del(name)
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey{}, net.ParseIP(ip).String())))
		})
	}
}
