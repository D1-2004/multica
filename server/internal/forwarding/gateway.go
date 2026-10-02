package forwarding

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// Gateway is immutable after construction and safe across concurrent requests.
// Registries are deployment configuration; no browser value becomes a URL.
type Gateway struct {
	targets   map[string]*url.URL
	transport http.RoundTripper
	slots     chan struct{}
}

func New(targets map[string]string, transport http.RoundTripper) (*Gateway, error) {
	g := &Gateway{targets: make(map[string]*url.URL), slots: make(chan struct{}, 128), transport: &http.Transport{
		Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: 90 * time.Second,
	}}
	if transport != nil {
		g.transport = transport
	}
	for name, raw := range targets {
		if !targetName.MatchString(name) {
			return nil, errors.New("invalid forwarding target name")
		}
		u, err := normalizeOrigin(raw)
		if err != nil {
			return nil, err
		}
		g.targets[name] = u
	}
	return g, nil
}

func ParseTargets(raw string) (map[string]string, error) {
	targets := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return targets, nil
	}
	if err := json.Unmarshal([]byte(raw), &targets); err != nil || targets == nil {
		return nil, errors.New("invalid MULTICA_FORWARD_TARGETS object")
	}
	_, err := New(targets, nil)
	return targets, err
}

func (g *Gateway) Matches(target, origin string) bool {
	if g == nil {
		return false
	}
	u := g.targets[target]
	return u != nil && u.String() == origin
}

func (g *Gateway) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, Prefix) && r.URL.Path != "/forward" {
			next.ServeHTTP(w, r)
			return
		}
		if !canonicalPath(r.URL) {
			http.Error(w, "invalid forwarding path", 400)
			return
		}
		name, rest, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, Prefix), "/")
		if !ok || !BrowserRoute(r.Method, "/"+rest) {
			http.NotFound(w, r)
			return
		}
		g.proxy(w, r, name, "/"+rest, "")
	})
}

// Callback forwards only after the OAuth adapter verified and consumed its
// signed registration. The exact state-bound cookie is the only credential
// allowed through this fixed (unprefixed) provider callback.
func (g *Gateway) Callback(w http.ResponseWriter, r *http.Request, target, p, cookieName string) {
	if r.Method != http.MethodGet || !callbackPath(p) || !strings.HasPrefix(cookieName, "multica_mcpc_") {
		http.Error(w, "invalid forwarding callback", 400)
		return
	}
	g.proxy(w, r, target, p, cookieName)
}

func (g *Gateway) proxy(w http.ResponseWriter, r *http.Request, name, p, bindingCookie string) {
	u := g.targets[name]
	if u == nil {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get(HopHeader) != "" || strings.EqualFold(r.Host, u.Host) {
		http.Error(w, "forwarding loop refused", 508)
		return
	}
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		http.Error(w, "forwarding at capacity", 503)
		return
	}
	mount := Prefix + name
	started := time.Now()
	proxy := &httputil.ReverseProxy{
		Transport: g.transport, FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(u)
			pr.Out.URL.Path = p
			pr.Out.URL.RawPath = ""
			pr.Out.Host = u.Host
			// ReverseProxy removes hop-by-hop headers, including Connection tokens.
			for key := range pr.Out.Header {
				low := strings.ToLower(key)
				if strings.HasPrefix(low, "x-user-") || strings.HasPrefix(low, "x-forwarded-") || strings.HasPrefix(low, "x-workspace-") || strings.HasPrefix(low, "x-multica-") {
					pr.Out.Header.Del(key)
				}
			}
			for _, key := range []string{"Authorization", "Cookie", "X-API-Key", "X-Actor-Source", "X-Auth-Method", "Forwarded", "X-Real-IP", "Origin", "Referer"} {
				pr.Out.Header.Del(key)
			}
			// Ignore all root/other-environment cookies. Duplicate credentials are
			// rejected by dropping them, never chosen by header order.
			seen := map[string]int{}
			for _, c := range pr.In.Cookies() {
				seen[c.Name]++
			}
			for _, c := range pr.In.Cookies() {
				if seen[c.Name] != 1 {
					continue
				}
				if bindingCookie != "" {
					if c.Name == bindingCookie {
						pr.Out.AddCookie(c)
					}
					continue
				}
				original, ok := strings.CutPrefix(c.Name, "mf_"+name+"_")
				if ok && (original == "multica_auth" || original == "multica_csrf") {
					c.Name = original
					pr.Out.AddCookie(c)
				}
			}
			pr.Out.Header.Set(HopHeader, name)
		},
		ModifyResponse: func(resp *http.Response) error {
			cookies := resp.Cookies()
			resp.Header.Del("Set-Cookie")
			for _, c := range cookies {
				c.Domain = ""
				c.Secure = true
				switch {
				case c.Name == "multica_auth" || c.Name == "multica_csrf":
					if bindingCookie != "" {
						continue
					}
					c.Name = "mf_" + name + "_" + c.Name
					c.Path = mount + "/"
				case strings.HasPrefix(c.Name, "multica_mcpc_") && callbackPath(c.Path):
					if bindingCookie != "" && c.Name != bindingCookie {
						continue
					}
				default:
					continue
				}
				resp.Header.Add("Set-Cookie", c.String())
			}
			if raw := resp.Header.Get("Location"); raw != "" {
				loc, err := url.Parse(raw)
				if err != nil {
					return errors.New("invalid upstream redirect")
				}
				if loc.Host == u.Host && loc.Scheme == u.Scheme || loc.Host == "" && strings.HasPrefix(loc.Path, "/") {
					loc.Scheme = ""
					loc.Host = ""
					loc.Path = mount + loc.Path
					loc.RawPath = ""
					resp.Header.Set("Location", loc.String())
				}
			}
			resp.Header.Set("Cache-Control", "no-store")
			resp.Header.Set("Referrer-Policy", "no-referrer")
			slog.InfoContext(r.Context(), "request forwarded", "event", "environment_forwarded", "target", name, "surface", "context_config", "method", r.Method, "status", resp.StatusCode, "duration_ms", time.Since(started).Milliseconds())
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
			slog.WarnContext(r.Context(), "forwarding upstream unavailable", "event", "environment_forward_failed", "target", name)
			http.Error(w, "forwarding target unavailable", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}
