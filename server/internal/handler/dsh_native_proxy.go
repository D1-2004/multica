package handler

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

const dshNativeProxyRoot = "/api/dsh-native/ui/"
const dshNativeProxyCookie = "__Secure-multica-dsh-native"
const dshNativeGatewayCookie = "__Host-MulticaDSH"

// NativeUI keeps the sandbox's public port behind the workbench origin. The
// persisted grant selects the upstream; a client cannot supply a host or URL.
// The gateway still authorizes every operation and live WebSocket connection.
func (h *Handler) DSHNativeUI(w http.ResponseWriter, r *http.Request) {
	dshNativeResponseHeaders(w)
	id, err := uuid.Parse(chi.URLParam(r, "accessId"))
	if err != nil || h.DB == nil || h.FCE2BLauncher == nil {
		writeError(w, http.StatusUnauthorized, "open DSH again from the employee workbench")
		return
	}
	access, err := (dshhost.PostgresStore{DB: h.DB}).LookupNativeAccess(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "open DSH again from the employee workbench")
		return
	}
	host := dshhost.Host{Key: access.Key, Generation: access.Generation, SandboxID: access.SandboxID, State: "running"}
	upstream, origin, err := h.FCE2BLauncher.DSHNativeProxyAddress(host)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "DSH native gateway is unavailable")
		return
	}
	prefix := dshNativeProxyRoot + id.String() + "/"
	if serveDSHNativeRestart(w, r, upstream, origin, prefix, func() error {
		if _, err := h.nativeProxyAuthorize(r, access); err != nil {
			return errors.New("open DSH again from the employee workbench")
		}
		return h.FCE2BLauncher.PrepareDSHNativeRestart(r.Context(), host)
	}) {
		return
	}
	if h.routeDSHNativeRequest(w, r, access, origin, prefix) {
		return
	}
	serveDSHNativeProxy(w, r, upstream, origin, prefix)
}

func serveDSHNativeRestart(w http.ResponseWriter, r *http.Request, upstream, origin, prefix string, prepare func() error) bool {
	path := "/" + strings.TrimPrefix(r.URL.Path, prefix)
	if path != "/dsh-market/restart" && path != "/dsh-market/api/v1/restart" {
		return false
	}
	if !strings.HasPrefix(r.URL.Path, prefix) || r.Method != http.MethodPost || r.URL.RawQuery != "" || r.Header.Get("Upgrade") != "" || !nativeRequestOrigin(r, origin) {
		writeError(w, http.StatusForbidden, "invalid DSH restart request")
		return true
	}
	if err := prepare(); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return true
	}
	serveDSHNativeProxy(w, r, upstream, origin, prefix)
	return true
}

func serveDSHNativeProxy(w http.ResponseWriter, r *http.Request, upstream, publicOrigin, prefix string) {
	target, err := url.Parse(upstream)
	if err != nil || !strings.HasPrefix(r.URL.Path, prefix) || r.Header.Get("Authorization") != "" ||
		(r.Header.Get("Origin") != "" && r.Header.Get("Origin") != publicOrigin) ||
		(r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("Origin") != publicOrigin) ||
		(strings.EqualFold(r.Header.Get("Upgrade"), "websocket") && r.Header.Get("Origin") != publicOrigin) ||
		r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeError(w, http.StatusForbidden, "invalid DSH native request origin")
		return
	}
	path := "/" + strings.TrimPrefix(r.URL.Path, prefix)
	// Internal authority polling is never a browser surface.
	if strings.HasPrefix(path, "/_multica/") && path != "/_multica/open" && path != "/_multica/entry" {
		writeError(w, http.StatusNotFound, "native route unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(target)
			p.Out.URL.Path, p.Out.URL.RawPath = path, ""
			p.Out.Host = target.Host
			// No workbench session, authorization, forwarding or cloud metadata
			// may enter a plugin. Only this grant's scoped native cookie crosses.
			for key := range p.Out.Header {
				lower := strings.ToLower(key)
				if lower == "cookie" || lower == "authorization" || lower == "referer" || lower == "forwarded" ||
					strings.HasPrefix(lower, "x-") || lower == "fc-affinity-session-id" {
					p.Out.Header.Del(key)
				}
			}
			if cookie, err := p.In.Cookie(dshNativeProxyCookie); err == nil {
				p.Out.AddCookie(&http.Cookie{Name: dshNativeGatewayCookie, Value: cookie.Value})
			}
			p.Out.Header.Set("Origin", upstream)
			p.Out.Header.Set("Accept-Encoding", "identity")
		},
		ModifyResponse: func(response *http.Response) error {
			response.Header.Del("Content-Disposition")
			response.Header.Del("Access-Control-Allow-Origin")
			response.Header.Del("Access-Control-Allow-Credentials")
			cookies := response.Cookies()
			response.Header.Del("Set-Cookie")
			for _, cookie := range cookies {
				if cookie.Name == dshNativeGatewayCookie {
					cookie.Name, cookie.Path, cookie.Domain = dshNativeProxyCookie, prefix, ""
					cookie.Secure, cookie.HttpOnly, cookie.SameSite = true, true, http.SameSiteStrictMode
					response.Header.Add("Set-Cookie", cookie.String())
				}
			}
			response.Header.Set("Cache-Control", "no-store")
			response.Header.Set("Referrer-Policy", "no-referrer")
			if response.StatusCode == http.StatusSwitchingProtocols {
				return nil
			}
			kind := strings.Split(response.Header.Get("Content-Type"), ";")[0]
			if kind != "text/html" && kind != "text/css" {
				return nil
			}
			if response.Header.Get("Content-Encoding") != "" {
				return errors.New("native page encoding unavailable")
			}
			body, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
			response.Body.Close()
			if err != nil || len(body) > 8<<20 {
				return errors.New("native page unavailable")
			}
			body = rewriteDSHNativePage(body, prefix, kind == "text/html")
			if kind == "text/html" {
				var random [18]byte
				if _, err := rand.Read(random[:]); err != nil {
					return err
				}
				nonce := base64.RawStdEncoding.EncodeToString(random[:])
				body = nativeScriptTag.ReplaceAll(body, []byte(`<script nonce="`+nonce+`"${1}`))
				// Replace the workbench API policy and the upstream bootstrap
				// policy with one nonce policy for this rewritten native document.
				// The official Cordis client compiles expressions with Function at
				// module boot; eval is confined to this native document policy.
				response.Header.Del("Content-Security-Policy")
				w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-eval' 'nonce-"+nonce+"'; style-src 'self' 'unsafe-inline'; img-src 'self' https: data:; font-src 'self' data:; connect-src 'self' wss:; worker-src 'self' blob:; frame-ancestors 'none'; object-src 'none'; base-uri 'none'; form-action 'self'")
			}
			response.Body = io.NopCloser(bytes.NewReader(body))
			response.ContentLength = int64(len(body))
			response.Header.Set("Content-Length", strconv.Itoa(len(body)))
			response.Header.Del("ETag")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			writeError(w, http.StatusBadGateway, "DSH native gateway is temporarily unavailable")
		},
		FlushInterval: -1,
	}
	proxy.ServeHTTP(w, r)
}

var nativeScriptTag = regexp.MustCompile(`(?i)<script(\s|>)`)

var nativeRootReference = regexp.MustCompile(`(["'])/([^/])`)

func rewriteDSHNativePage(body []byte, prefix string, html bool) []byte {
	// Native boot data includes import-map/module URLs as well as HTML attrs.
	// Relative module imports then resolve naturally under this same prefix.
	body = nativeRootReference.ReplaceAll(body, []byte("${1}"+prefix+"${2}"))
	body = bytes.ReplaceAll(body, []byte("url(/"), []byte("url("+prefix))
	if !html {
		return body
	}
	// DSH's browser transport builds origin-relative RPC/stream URLs. Adapt
	// those browser carriers once, preserving request bodies and native APIs.
	script := `<script>(()=>{const base=` + strconv.Quote(prefix) + `;
const map=value=>{const u=new URL(String(value),location.href);if(u.host===location.host&&!u.pathname.startsWith(base))u.pathname=base+u.pathname.replace(/^\//,'');return u.href};
const fetch=globalThis.fetch;globalThis.fetch=(input,init)=>fetch.call(globalThis,input instanceof Request?new Request(map(input.url),input):map(input),init);
for(const name of ['WebSocket','EventSource','Worker']){const Original=globalThis[name];if(Original)globalThis[name]=class extends Original{constructor(url,...args){super(map(url),...args)}}}
const open=XMLHttpRequest.prototype.open;XMLHttpRequest.prototype.open=function(method,url,...args){return open.call(this,method,map(url),...args)};
})();</script>`
	if at := bytes.Index(bytes.ToLower(body), []byte("<head>")); at >= 0 {
		at += len("<head>")
		return append(append(append([]byte{}, body[:at]...), []byte(script)...), body[at:]...)
	}
	return append([]byte(script), body...)
}
