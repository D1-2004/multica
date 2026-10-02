// Package forwarding owns transport policy for requests entering through a
// stable public origin. Routing never grants application authorization.
package forwarding

import (
	"errors"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
)

const Prefix = "/forward/"
const HopHeader = "X-Multica-Forwarded-By"

var targetName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// ParsePublicBase accepts a canonical, explicitly configured browser mount.
func ParsePublicBase(raw string) (origin, target string, err error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return "", "", errors.New("invalid forwarding public base")
	}
	target = strings.TrimPrefix(u.Path, Prefix)
	if u.Path != Prefix+target || !targetName.MatchString(target) {
		return "", "", errors.New("public base must end in /forward/{target}")
	}
	if _, e = normalizeOrigin("https://" + u.Host); e != nil {
		return "", "", e
	}
	return "https://" + u.Host, target, nil
}

func normalizeOrigin(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(raw, "\\ \t\r\n") {
		return nil, errors.New("forwarding target must be an HTTPS origin")
	}
	u.Path = ""
	return u, nil
}

func canonicalPath(u *url.URL) bool {
	if !strings.HasPrefix(u.Path, "/") || strings.ContainsAny(u.Path, "\\\x00\r\n") || strings.Contains(u.Path, "//") || path.Clean(u.Path) != u.Path {
		return false
	}
	escaped := strings.ToLower(u.EscapedPath())
	for _, bad := range []string{"%2f", "%5c", "%25"} {
		if strings.Contains(escaped, bad) {
			return false
		}
	}
	return true
}

// BrowserRoute is the single allow-list for the configuration surface. New
// products opt in with a named policy rather than inheriting all /api routes.
func BrowserRoute(method, p string) bool {
	read := method == http.MethodGet || method == http.MethodHead
	if read && (p == "/dingtalk/configure" || strings.HasPrefix(p, "/_next/static/") || p == "/favicon.svg") {
		return true
	}
	if method == http.MethodGet && (p == "/api/config" || p == "/api/me" || p == "/api/dingtalk/jsapi-config") {
		return true
	}
	if method == http.MethodPost && (p == "/auth/fde/dingtalk" || p == "/auth/logout" || p == "/api/context-capabilities/links/redeem") {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(p, "/api/context-capabilities/"), "/")
	if !strings.HasPrefix(p, "/api/context-capabilities/") || len(parts) == 0 || parts[0] != "agents" {
		return false
	}
	if len(parts) == 1 {
		return method == http.MethodGet
	}
	if parts[1] == "" {
		return false
	}
	if len(parts) == 2 {
		return method == http.MethodGet
	}
	if len(parts) == 3 {
		switch parts[2] {
		case "bindings", "prompts", "mcp-config":
			return method == http.MethodPut
		case "credentials":
			return method == http.MethodPut || method == http.MethodDelete
		case "routines":
			return method == http.MethodGet || method == http.MethodPost
		}
	}
	if len(parts) == 4 {
		switch parts[2] {
		case "scenes":
			if parts[3] == "resolve" {
				return method == http.MethodPost
			}
			return parts[3] != "" && method == http.MethodGet
		case "connections":
			return parts[3] == "start" && method == http.MethodPost
		case "routines":
			return parts[3] != "" && (method == http.MethodPatch || method == http.MethodDelete)
		}
	}
	if len(parts) == 5 && parts[2] == "routines" && parts[3] != "" {
		if parts[4] == "runs" {
			return method == http.MethodGet
		}
		return (parts[4] == "run" || parts[4] == "rotate-webhook") && method == http.MethodPost
	}
	return false
}

func callbackPath(p string) bool {
	return p == "/api/connectors/oauth/callback" || p == "/api/connector-oauth/callback" || p == "/api/github/authorize"
}
