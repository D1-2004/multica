package forwarding

import (
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// LocalAssets serves this deployment's build under its public asset namespace
// even on its native host. Only the configured loopback frontend can be used;
// this route neither handles application requests nor relays credentials.
func LocalAssets(target, frontend string, next http.Handler) (http.Handler, error) {
	u, err := url.Parse(frontend)
	if err != nil || u.Scheme != "http" || !targetName.MatchString(target) || u.User != nil || u.RawQuery != "" || u.Path != "" || u.Fragment != "" {
		return nil, errors.New("invalid local frontend")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("local frontend must be loopback")
	}
	prefix := Prefix + target
	proxy := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(u)
		pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, prefix)
		pr.Out.URL.RawPath = ""
		pr.Out.Host = u.Host
		pr.Out.Header.Del("Cookie")
		pr.Out.Header.Del("Authorization")
	}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method == http.MethodGet || r.Method == http.MethodHead) && (strings.HasPrefix(r.URL.Path, prefix+"/_next/static/") || r.URL.Path == prefix+"/favicon.svg") {
			if !canonicalPath(r.URL) {
				http.Error(w, "invalid asset path", 400)
				return
			}
			proxy.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	}), nil
}

// AssetTarget validates the immutable build namespace independently of the
// public-link switch, so disabling new links never breaks the native app.
func AssetTarget(assetPrefix, publicBase string) (string, error) {
	var target string
	if assetPrefix != "" {
		_, parsed, err := ParsePublicBase("https://build.invalid" + assetPrefix)
		if err != nil {
			return "", errors.New("invalid forwarding build asset prefix")
		}
		target = parsed
	}
	if publicBase != "" {
		_, publicTarget, err := ParsePublicBase(publicBase)
		if err != nil {
			return "", err
		}
		if target != publicTarget {
			return "", errors.New("forwarding public base requires a matching MULTICA_FORWARD_ASSET_PREFIX build")
		}
	}
	return target, nil
}
