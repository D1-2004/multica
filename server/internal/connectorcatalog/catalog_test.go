package connectorcatalog

import (
	"net/url"
	"testing"
)

func TestDefaultCatalogHasGitHubAndValidEntries(t *testing.T) {
	apps := Default().Apps()
	want := []string{"github", "notion", "linear", "atlassian", "sentry", "asana", "figma", "stripe"}
	if len(apps) != len(want) {
		t.Fatalf("apps = %d, want %d", len(apps), len(want))
	}
	for i, slug := range want {
		if apps[i].Slug != slug {
			t.Fatalf("app %d = %q, want %q", i, apps[i].Slug, slug)
		}
	}
	github, ok := Default().Lookup("github")
	if !ok || github.AuthKind != AuthOAuthGitHubApp || !github.AllowsPAT || github.MCPURL != "https://api.githubcopilot.com/mcp/" {
		t.Fatalf("github entry = %+v", github)
	}
	if github.OAuthAvailable(false) || !github.OAuthAvailable(true) {
		t.Fatal("GitHub OAuth availability must follow the GitHub App configuration")
	}
	notion, _ := Default().Lookup("notion")
	if !notion.OAuthAvailable(false) || notion.AllowsPAT {
		t.Fatalf("notion entry = %+v", notion)
	}
	asana, ok := Default().Lookup("asana")
	if !ok || asana.AuthKind != AuthOAuthPreregistered || asana.AuthorizationEndpoint == "" || asana.TokenEndpoint == "" {
		t.Fatalf("asana entry = %+v", asana)
	}
	if asana.OAuthAvailable(true) {
		t.Fatal("Asana OAuth availability is per workspace, not the process environment")
	}
	for _, app := range apps {
		if slug, ok := Default().SlugForURL(app.MCPURL); !ok || slug != app.Slug {
			t.Fatalf("SlugForURL(%q) = %q %v", app.MCPURL, slug, ok)
		}
	}
}

func TestSlugForURLIsExact(t *testing.T) {
	for _, raw := range []string{
		"https://api.githubcopilot.com/mcp",
		"https://api.githubcopilot.com/mcp/readonly",
		"https://API.githubcopilot.com/mcp/",
		"https://api.githubcopilot.com:443/mcp/",
		"https://evil.example/mcp",
	} {
		if slug, ok := Default().SlugForURL(raw); ok {
			t.Fatalf("SlugForURL(%q) matched %q", raw, slug)
		}
	}
}

func TestNewRejectsUnsafeEntries(t *testing.T) {
	base := App{Slug: "demo", Name: "Demo", MCPURL: "https://mcp.demo.example/mcp", AuthKind: AuthOAuthDCR, Hosts: []string{"mcp.demo.example"}}
	if _, err := New(base); err != nil {
		t.Fatalf("valid app rejected: %v", err)
	}
	cases := map[string]func(*App){
		"http url":         func(a *App) { a.MCPURL = "http://mcp.demo.example/mcp" },
		"host outside":     func(a *App) { a.MCPURL = "https://other.example/mcp" },
		"query":            func(a *App) { a.MCPURL = "https://mcp.demo.example/mcp?x=1" },
		"userinfo":         func(a *App) { a.MCPURL = "https://u@mcp.demo.example/mcp" },
		"bad slug":         func(a *App) { a.Slug = "Demo!" },
		"no hosts":         func(a *App) { a.Hosts = nil },
		"wildcard host":    func(a *App) { a.Hosts = []string{"*.demo.example"} },
		"unknown kind":     func(a *App) { a.AuthKind = "password" },
		"dcr fixed tokens": func(a *App) { a.TokenEndpoint = "https://mcp.demo.example/token" },
		"github no token": func(a *App) {
			a.AuthKind = AuthOAuthGitHubApp
			a.AuthorizationEndpoint = "https://mcp.demo.example/authorize"
		},
	}
	for name, mutate := range cases {
		app := base
		app.Hosts = append([]string(nil), base.Hosts...)
		mutate(&app)
		if _, err := New(app); err == nil {
			t.Errorf("%s: accepted %+v", name, app)
		}
	}
	if _, err := New(base, base); err == nil {
		t.Fatal("duplicate slug accepted")
	}
}

func TestHostAllowedPortRules(t *testing.T) {
	hosts := []string{"mcp.demo.example", "127.0.0.1:8443"}
	for raw, want := range map[string]bool{
		"https://mcp.demo.example/mcp":      true,
		"https://mcp.demo.example:443/mcp":  true,
		"https://MCP.demo.example./mcp":     true,
		"https://mcp.demo.example:8443/mcp": false,
		"https://127.0.0.1:8443/token":      true,
		"https://127.0.0.1/token":           false,
		"https://evil.mcp.demo.example/mcp": false,
	} {
		u, _ := url.Parse(raw)
		if got := HostAllowed(u, hosts); got != want {
			t.Errorf("HostAllowed(%q) = %v, want %v", raw, got, want)
		}
	}
}
