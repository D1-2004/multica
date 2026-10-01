// Package connectorcatalog is the static registry of official remote MCP
// servers ("official apps") a workspace can add with one click. Each entry
// fixes the MCP endpoint, how people authorize it and the exact hosts the
// server may contact on its behalf. Catalog connectors are the only
// internal connectors that bypass the deployment's
// MULTICA_INTERNAL_MCP_ALLOWED_HOST_SUFFIXES allowlist, and only for their own
// template URL (see docs/internal-mcp-connectors.md).
package connectorcatalog

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// AuthKind says how people authorize an official app.
type AuthKind string

const (
	// AuthOAuthDCR uses MCP OAuth discovery, RFC 7591 dynamic client
	// registration and an S256 PKCE authorization code flow.
	AuthOAuthDCR AuthKind = "oauth_dcr"
	// AuthOAuthGitHubApp uses the deployment's pre-registered GitHub App
	// (GITHUB_APP_CLIENT_ID / GITHUB_APP_CLIENT_SECRET) and its registered
	// /api/github/authorize callback. GitHub has no dynamic registration.
	AuthOAuthGitHubApp AuthKind = "oauth_github_app"
	// AuthOAuthPreregistered uses a workspace-configured confidential client
	// and the fixed authorization and token endpoints on the app. The
	// provider has no dynamic registration. Asana MCP is this kind.
	AuthOAuthPreregistered AuthKind = "oauth_preregistered"
)

// App is one official app. Hosts lists every host the server contacts for
// the app (MCP endpoint, OAuth metadata, registration and token endpoints,
// the account endpoint) plus the browser-facing authorization host; each
// entry is an exact hostname (default HTTPS port only) or host:port.
type App struct {
	Slug      string
	Name      string
	MCPURL    string
	AuthKind  AuthKind
	AllowsPAT bool
	// Scope is the OAuth scope requested at authorization; it may be empty.
	Scope string
	Hosts []string
	// AuthorizationEndpoint and TokenEndpoint are fixed for pre-registered
	// clients (AuthOAuthGitHubApp, AuthOAuthPreregistered); DCR apps
	// discover them at runtime.
	AuthorizationEndpoint string
	TokenEndpoint         string
	// Resource is the RFC 8707 resource indicator sent on authorize, token
	// and refresh. Empty for providers that reject it (Slack). Asana MCP
	// issues an API token, which mcp.asana.com rejects, unless this is
	// https://mcp.asana.com/v2.
	Resource string
	// AccountURL answers GET with a JSON object whose "login" names the
	// connected account (GitHub). Empty when the app has no such endpoint.
	AccountURL string
}

// OAuthAvailable reports whether the OAuth connect flow can start for this
// app. A GitHub App flow needs the deployment's GitHub App client
// credentials; DCR apps register a client themselves.
func (a App) OAuthAvailable(githubAppConfigured bool) bool {
	switch a.AuthKind {
	case AuthOAuthDCR:
		return true
	case AuthOAuthGitHubApp:
		return githubAppConfigured
	case AuthOAuthPreregistered:
		// The client id and secret live on the workspace, not in the
		// process environment. Availability is decided per workspace.
		return false
	default:
		return false
	}
}

// Catalog is an immutable, validated set of apps.
type Catalog struct {
	apps   []App
	bySlug map[string]App
	byURL  map[string]string
}

var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

// New validates apps and returns a catalog in the given order.
func New(apps ...App) (*Catalog, error) {
	c := &Catalog{bySlug: map[string]App{}, byURL: map[string]string{}}
	for _, app := range apps {
		app.Hosts = normalizeHosts(app.Hosts)
		if err := validateApp(app); err != nil {
			return nil, fmt.Errorf("connector catalog app %q: %w", app.Slug, err)
		}
		if _, exists := c.bySlug[app.Slug]; exists {
			return nil, fmt.Errorf("connector catalog app %q: duplicate slug", app.Slug)
		}
		if _, exists := c.byURL[app.MCPURL]; exists {
			return nil, fmt.Errorf("connector catalog app %q: duplicate MCP URL", app.Slug)
		}
		c.apps = append(c.apps, app)
		c.bySlug[app.Slug] = app
		c.byURL[app.MCPURL] = app.Slug
	}
	return c, nil
}

// Apps returns the apps in catalog order. The slice is a copy.
func (c *Catalog) Apps() []App {
	if c == nil {
		return nil
	}
	out := make([]App, len(c.apps))
	for i, app := range c.apps {
		app.Hosts = append([]string(nil), app.Hosts...)
		out[i] = app
	}
	return out
}

// Lookup returns the app with slug.
func (c *Catalog) Lookup(slug string) (App, bool) {
	if c == nil {
		return App{}, false
	}
	app, ok := c.bySlug[slug]
	if ok {
		app.Hosts = append([]string(nil), app.Hosts...)
	}
	return app, ok
}

// SlugForURL returns the slug whose template MCP URL is exactly raw.
func (c *Catalog) SlugForURL(raw string) (string, bool) {
	if c == nil {
		return "", false
	}
	slug, ok := c.byURL[raw]
	return slug, ok
}

func validateApp(app App) error {
	if !slugPattern.MatchString(app.Slug) {
		return errors.New("invalid slug")
	}
	if name := strings.TrimSpace(app.Name); name == "" || len(name) > 120 {
		return errors.New("invalid name")
	}
	if len(app.Hosts) == 0 {
		return errors.New("no allowed hosts")
	}
	for _, host := range app.Hosts {
		if !validHostEntry(host) {
			return fmt.Errorf("invalid host %q", host)
		}
	}
	if err := checkURL(app.MCPURL, app.Hosts, false); err != nil {
		return fmt.Errorf("mcp url: %w", err)
	}
	switch app.AuthKind {
	case AuthOAuthDCR:
		if app.AuthorizationEndpoint != "" || app.TokenEndpoint != "" {
			return errors.New("DCR apps discover their OAuth endpoints")
		}
	case AuthOAuthGitHubApp, AuthOAuthPreregistered:
		if err := checkURL(app.AuthorizationEndpoint, app.Hosts, true); err != nil {
			return fmt.Errorf("authorization endpoint: %w", err)
		}
		if err := checkURL(app.TokenEndpoint, app.Hosts, false); err != nil {
			return fmt.Errorf("token endpoint: %w", err)
		}
		if app.Resource != "" {
			if err := checkURL(app.Resource, app.Hosts, false); err != nil {
				return fmt.Errorf("resource: %w", err)
			}
		}
	default:
		return errors.New("unknown auth kind")
	}
	if app.AccountURL != "" {
		if err := checkURL(app.AccountURL, app.Hosts, false); err != nil {
			return fmt.Errorf("account url: %w", err)
		}
	}
	if strings.ContainsAny(app.Scope, "\r\n\x00") || len(app.Scope) > 512 {
		return errors.New("invalid scope")
	}
	return nil
}

// checkURL requires an https URL without userinfo or fragment whose host is
// one of hosts. MCP and token URLs must not carry a query.
func checkURL(raw string, hosts []string, allowQuery bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || (!allowQuery && u.RawQuery != "") {
		return errors.New("must be an https URL without userinfo, fragment or query")
	}
	if !HostAllowed(u, hosts) {
		return errors.New("host is not in the app's host list")
	}
	return nil
}

// HostAllowed reports whether u's host is one of hosts. A bare hostname
// entry matches only the default HTTPS port; a host:port entry matches that
// exact port.
func HostAllowed(u *url.URL, hosts []string) bool {
	if u == nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	port := u.Port()
	for _, entry := range hosts {
		entryHost, entryPort, hasPort := splitHostEntry(entry)
		if host != entryHost {
			continue
		}
		if hasPort {
			if port == entryPort {
				return true
			}
			continue
		}
		if port == "" || port == "443" {
			return true
		}
	}
	return false
}

func splitHostEntry(entry string) (string, string, bool) {
	if host, port, err := net.SplitHostPort(entry); err == nil {
		return host, port, true
	}
	return entry, "", false
}

func validHostEntry(entry string) bool {
	host, port, hasPort := splitHostEntry(entry)
	if host == "" || strings.ContainsAny(host, "/@?#*") {
		return false
	}
	if hasPort && (port == "" || strings.Trim(port, "0123456789") != "") {
		return false
	}
	return true
}

func normalizeHosts(hosts []string) []string {
	out := make([]string, 0, len(hosts))
	seen := map[string]bool{}
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if h, port, err := net.SplitHostPort(host); err == nil {
			host = net.JoinHostPort(strings.TrimSuffix(h, "."), port)
		} else {
			host = strings.TrimSuffix(host, ".")
		}
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		out = append(out, host)
	}
	return out
}
