package connectorcatalog

// slackUserScopes are the user-token scopes Slack's MCP server documents
// for search, history and sending. The user authorize endpoint takes them
// comma-separated. A bot token cannot call mcp.slack.com.
const slackUserScopes = "search:read.public,search:read.private,search:read.im,search:read.mpim,search:read.files,search:read.users,channels:history,groups:history,im:history,mpim:history,channels:read,groups:read,im:read,mpim:read,chat:write,files:read,users:read,emoji:read,reactions:read"

// defaultApps are the official remote MCP servers offered to every
// workspace. Endpoints and OAuth hosts were probed on 2026-09-30: every DCR
// server answers an unauthenticated initialize with 401 + WWW-Authenticate
// and advertises S256 PKCE; GitHub's authorization server
// (https://github.com/login/oauth) has no dynamic registration, so it uses
// the deployment's GitHub App. Slack's MCP server also has no dynamic
// registration; the workspace settings hold its confidential client.
var defaultApps = []App{
	{
		Slug: "github", Name: "GitHub", MCPURL: "https://api.githubcopilot.com/mcp/",
		AuthKind: AuthOAuthGitHubApp, AllowsPAT: true,
		Hosts:                 []string{"api.githubcopilot.com", "github.com", "api.github.com"},
		AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
		TokenEndpoint:         "https://github.com/login/oauth/access_token",
		AccountURL:            "https://api.github.com/user",
	},
	{
		// User tokens only. oauth.v2.access returns a bot token and is not
		// a standard token response; oauth.v2.user.access is.
		Slug: "slack", Name: "Slack", MCPURL: "https://mcp.slack.com/mcp",
		AuthKind:              AuthOAuthPreregistered,
		Scope:                 slackUserScopes,
		Hosts:                 []string{"mcp.slack.com", "slack.com"},
		AuthorizationEndpoint: "https://slack.com/oauth/v2_user/authorize",
		TokenEndpoint:         "https://slack.com/api/oauth.v2.user.access",
	},
	{
		Slug: "notion", Name: "Notion", MCPURL: "https://mcp.notion.com/mcp",
		AuthKind: AuthOAuthDCR, Hosts: []string{"mcp.notion.com"},
	},
	{
		Slug: "linear", Name: "Linear", MCPURL: "https://mcp.linear.app/mcp",
		AuthKind: AuthOAuthDCR, Scope: "read write", Hosts: []string{"mcp.linear.app"},
	},
	{
		// No resource_metadata in the challenge and no protected resource
		// document: the authorization server metadata lives at the MCP origin.
		Slug: "atlassian", Name: "Atlassian", MCPURL: "https://mcp.atlassian.com/v1/mcp",
		AuthKind: AuthOAuthDCR, Hosts: []string{"mcp.atlassian.com"},
	},
	{
		Slug: "sentry", Name: "Sentry", MCPURL: "https://mcp.sentry.dev/mcp",
		AuthKind: AuthOAuthDCR, Hosts: []string{"mcp.sentry.dev"},
	},
	{
		// Asana's authorization server (app.asana.com) does not advertise a
		// registration_endpoint, so dynamic registration cannot start.
		// Workspace settings hold the pre-registered MCP app's client id and
		// secret. The catalog template stays https://mcp.asana.com/mcp so
		// connectors already added still match; /v2/mcp is the same host.
		Slug: "asana", Name: "Asana", MCPURL: "https://mcp.asana.com/mcp",
		AuthKind:              AuthOAuthPreregistered,
		Hosts:                 []string{"mcp.asana.com", "app.asana.com"},
		AuthorizationEndpoint: "https://app.asana.com/-/oauth_authorize",
		TokenEndpoint:         "https://app.asana.com/-/oauth_token",
	},
	{
		// Authorization server api.figma.com, browser consent on
		// www.figma.com; confidential clients only (no "none" auth method).
		Slug: "figma", Name: "Figma", MCPURL: "https://mcp.figma.com/mcp",
		AuthKind: AuthOAuthDCR, Scope: "mcp:connect", Hosts: []string{"mcp.figma.com", "api.figma.com", "www.figma.com"},
	},
	{
		// The protected resource is the origin; the authorization server is
		// https://access.stripe.com/mcp.
		Slug: "stripe", Name: "Stripe", MCPURL: "https://mcp.stripe.com/",
		AuthKind: AuthOAuthDCR, Hosts: []string{"mcp.stripe.com", "access.stripe.com"},
	},
}

var defaultCatalog = mustNew(defaultApps...)

// Default returns the built-in catalog.
func Default() *Catalog { return defaultCatalog }

func mustNew(apps ...App) *Catalog {
	c, err := New(apps...)
	if err != nil {
		panic(err)
	}
	return c
}
