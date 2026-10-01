package connectorcatalog

// ProductionCallbackURL is the callback an admin pastes into a provider
// console. Pre-release deployments forward this callback back to themselves.
const ProductionCallbackURL = "https://fde-workbench.dingtalk.com/api/connectors/oauth/callback"

// SettingsField is one value an admin types for a pre-registered app.
// Authorization endpoints, scopes and the MCP URL are not fields.
type SettingsField struct {
	Key      string `json:"key"`
	Optional bool   `json:"optional"`
	File     bool   `json:"file"`
}

// SettingsSpec is one row of the workspace connector settings page.
// Mode is preregistered (the admin fills fields), dcr (nothing to fill) or
// limited (dynamic registration is refused by the provider).
type SettingsSpec struct {
	Slug                  string          `json:"slug"`
	Name                  string          `json:"name"`
	Mode                  string          `json:"mode"`
	Later                 bool            `json:"later"`
	Fields                []SettingsField `json:"fields"`
	DocsURL               string          `json:"docs_url"`
	AuthorizationEndpoint string          `json:"authorization_endpoint,omitempty"`
	TokenEndpoint         string          `json:"token_endpoint,omitempty"`
	Scopes                string          `json:"scopes,omitempty"`
	MCPURL                string          `json:"mcp_url,omitempty"`
	KnownClientID         string          `json:"known_client_id,omitempty"`
	// OAuthConnect is true when 连接 can start the catalog OAuth flow.
	OAuthConnect bool `json:"oauth_connect"`
}

// SettingsCatalog is the settings page, in display order. It includes apps
// that are not remote MCP servers yet (Slack, Google, Dropbox, Box); those
// rows store credentials only.
func SettingsCatalog() []SettingsSpec {
	return []SettingsSpec{
		{
			Slug: "github", Name: "GitHub", Mode: "preregistered", OAuthConnect: true,
			DocsURL:               "https://github.com/settings/apps",
			AuthorizationEndpoint: "https://github.com/login/oauth/authorize",
			TokenEndpoint:         "https://github.com/login/oauth/access_token",
			MCPURL:                "https://api.githubcopilot.com/mcp/",
			Fields: []SettingsField{
				{Key: "app_id"},
				{Key: "app_slug"},
				{Key: "client_id"},
				{Key: "client_secret"},
				{Key: "private_key", File: true},
				{Key: "webhook_secret", Optional: true},
			},
		},
		{
			Slug: "slack", Name: "Slack", Mode: "preregistered",
			DocsURL:               "https://api.slack.com/apps",
			AuthorizationEndpoint: "https://slack.com/oauth/v2/authorize",
			TokenEndpoint:         "https://slack.com/api/oauth.v2.access",
			Fields: []SettingsField{
				{Key: "client_id"},
				{Key: "client_secret"},
				{Key: "signing_secret", Optional: true},
			},
		},
		{
			Slug: "asana", Name: "Asana", Mode: "preregistered", OAuthConnect: true,
			DocsURL:               "https://app.asana.com/0/my-apps",
			AuthorizationEndpoint: "https://app.asana.com/-/oauth_authorize",
			TokenEndpoint:         "https://app.asana.com/-/oauth_token",
			MCPURL:                "https://mcp.asana.com/mcp",
			KnownClientID:         "1219049145290369",
			Fields: []SettingsField{
				{Key: "client_id"},
				{Key: "client_secret"},
			},
		},
		{
			Slug: "google", Name: "Google", Mode: "preregistered",
			DocsURL:               "https://console.cloud.google.com/apis/credentials",
			AuthorizationEndpoint: "https://accounts.google.com/o/oauth2/v2/auth",
			TokenEndpoint:         "https://oauth2.googleapis.com/token",
			Scopes:                "https://www.googleapis.com/auth/drive https://www.googleapis.com/auth/gmail.modify https://www.googleapis.com/auth/calendar",
			Fields: []SettingsField{
				{Key: "client_id"},
				{Key: "client_secret"},
			},
		},
		{
			Slug: "dropbox", Name: "Dropbox", Mode: "preregistered", Later: true,
			DocsURL:               "https://www.dropbox.com/developers/apps",
			AuthorizationEndpoint: "https://www.dropbox.com/oauth2/authorize",
			TokenEndpoint:         "https://api.dropboxapi.com/oauth2/token",
			Fields: []SettingsField{
				{Key: "client_id"},
				{Key: "client_secret"},
			},
		},
		{
			Slug: "box", Name: "Box", Mode: "preregistered", Later: true,
			DocsURL:               "https://app.box.com/developers/console",
			AuthorizationEndpoint: "https://account.box.com/api/oauth2/authorize",
			TokenEndpoint:         "https://api.box.com/oauth2/token",
			Fields: []SettingsField{
				{Key: "client_id"},
				{Key: "client_secret"},
			},
		},
		{Slug: "notion", Name: "Notion", Mode: "dcr", MCPURL: "https://mcp.notion.com/mcp"},
		{Slug: "linear", Name: "Linear", Mode: "dcr", MCPURL: "https://mcp.linear.app/mcp"},
		{Slug: "sentry", Name: "Sentry", Mode: "dcr", MCPURL: "https://mcp.sentry.dev/mcp"},
		{Slug: "atlassian", Name: "Atlassian", Mode: "dcr", MCPURL: "https://mcp.atlassian.com/v1/mcp"},
		{Slug: "figma", Name: "Figma", Mode: "limited", MCPURL: "https://mcp.figma.com/mcp"},
		{Slug: "stripe", Name: "Stripe", Mode: "dcr", MCPURL: "https://mcp.stripe.com/"},
	}
}

// SettingsSpecFor returns the settings row for slug.
func SettingsSpecFor(slug string) (SettingsSpec, bool) {
	for _, spec := range SettingsCatalog() {
		if spec.Slug == slug {
			return spec, true
		}
	}
	return SettingsSpec{}, false
}
