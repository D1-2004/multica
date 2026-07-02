package handler

import (
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/analytics"
)

type AppConfig struct {
	CdnDomain string `json:"cdn_domain"`
	// CdnSigned tells clients that the CDN domain above serves PRIVATE
	// content through time-bounded signed URLs (CloudFront signing is
	// enabled). When true, a raw storage URL on the CDN domain is NOT
	// publicly fetchable — renderers must not pick it as a native
	// <img>/<video> source and should fall back to the per-attachment
	// API endpoint or a freshly signed download_url instead (MUL-3254).
	// Omitted when false so older clients see the previous shape.
	CdnSigned bool `json:"cdn_signed,omitempty"`
	// Public auth config consumed by the web app at runtime so self-hosted
	// deployments do not need to rebuild the frontend image when operators
	// toggle signup or wire Google OAuth.
	AllowSignup    bool   `json:"allow_signup"`
	GoogleClientID string `json:"google_client_id,omitempty"`
	// DingtalkClientID is the DingTalk app's AppKey (a.k.a. ClientId). When
	// set, the web app renders the "使用钉钉登录" button and builds the
	// login.dingtalk.com/oauth2/auth authorize URL with it. Omitted when
	// empty so responses stay identical to the previous shape.
	DingtalkClientID string `json:"dingtalk_client_id,omitempty"`
	// DingtalkOnly is kept for older installed clients: it is derived (true
	// only when the LoginProviders allowlist is exactly ["dingtalk"]) so a
	// desktop build that predates login_providers still collapses to the
	// DingTalk-only screen. New clients read LoginProviders instead.
	// Omitted when false to keep responses identical to the previous shape.
	DingtalkOnly bool `json:"dingtalk_only,omitempty"`
	// LoginProviders is the sign-in allowlist from LOGIN_PROVIDERS (e.g.
	// ["dingtalk","lark"]). It is the display half of the lock — the login
	// screen only offers the listed entry points — while the router refuses
	// to register the login routes of providers outside the list, so the
	// closed paths 404 server-side too. Since the DingTalk/Feishu apps are
	// 企业内部应用, their OAuth logins only admit members of the organization,
	// so an OAuth-only allowlist restricts access to the org. Omitted when
	// unrestricted to keep responses identical to the previous shape.
	LoginProviders []string `json:"login_providers,omitempty"`
	// LarkClientID is the Feishu app's AppID (cli_xxx). When set, the web app
	// renders the "使用飞书登录" button and builds the
	// accounts.feishu.cn/open-apis/authen/v1/authorize URL with it. Non-secret
	// — required on this backend even when the code exchange runs through the
	// private agent, because the login page needs it for the authorize URL
	// (same split as DINGTALK_CLIENT_ID). Omitted when empty.
	LarkClientID string `json:"lark_client_id,omitempty"`
	// WorkspaceCreationDisabled mirrors the server-side
	// DISABLE_WORKSPACE_CREATION env var so the UI can hide every
	// "Create workspace" affordance on self-hosted instances. Omitted
	// from the JSON when false to keep responses identical to the
	// previous shape for the common managed-cloud case (#3433).
	WorkspaceCreationDisabled bool `json:"workspace_creation_disabled,omitempty"`
	// Public daemon setup config consumed by the web app at runtime so
	// self-hosted instances can show `multica setup self-host` commands
	// with the operator's own domains instead of Multica Cloud defaults.
	DaemonServerURL string `json:"daemon_server_url,omitempty"`
	DaemonAppURL    string `json:"daemon_app_url,omitempty"`

	// PostHog public config for the frontend. The key is the same Project
	// API Key the backend uses; returning it here (instead of baking it
	// into the frontend bundle via NEXT_PUBLIC_*) means self-hosted
	// instances — whose server returns an empty key — automatically
	// disable frontend event shipping too.
	PosthogKey           string `json:"posthog_key"`
	PosthogHost          string `json:"posthog_host"`
	AnalyticsEnvironment string `json:"analytics_environment"`
}

// GetConfig is mounted on the public (unauthenticated) route group because
// the web app calls it before login to decide whether to render the Google
// sign-in button and signup UI. Only add fields here that are safe to expose
// to anonymous callers — never user- or tenant-scoped data.
func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	config := AppConfig{
		AllowSignup:               os.Getenv("ALLOW_SIGNUP") != "false",
		GoogleClientID:            os.Getenv("GOOGLE_CLIENT_ID"),
		DingtalkClientID:          os.Getenv("DINGTALK_CLIENT_ID"),
		LarkClientID:              os.Getenv("LARK_CLIENT_ID"),
		WorkspaceCreationDisabled: os.Getenv("DISABLE_WORKSPACE_CREATION") == "true",
	}
	providers := LoginProviders()
	config.LoginProviders = providers
	config.DingtalkOnly = len(providers) == 1 && providers[0] == "dingtalk"
	if h.Storage != nil {
		config.CdnDomain = h.Storage.CdnDomain()
	}
	config.CdnSigned = h.CFSigner != nil
	config.DaemonServerURL, config.DaemonAppURL = daemonSetupURLsFromEnv()

	// Re-read from env on every request so operators can rotate keys via
	// secret refresh without a server restart.
	if v := os.Getenv("ANALYTICS_DISABLED"); v != "true" && v != "1" {
		config.PosthogKey = os.Getenv("POSTHOG_API_KEY")
		config.PosthogHost = os.Getenv("POSTHOG_HOST")
		config.AnalyticsEnvironment = analytics.EnvironmentFromEnv()
		if config.PosthogHost == "" && config.PosthogKey != "" {
			config.PosthogHost = "https://us.i.posthog.com"
		}
	}

	writeJSON(w, http.StatusOK, config)
}

// knownLoginProviders enumerates the values accepted in LOGIN_PROVIDERS.
// Unknown entries are dropped so a typo narrows the lock instead of
// silently opening a provider.
var knownLoginProviders = map[string]bool{
	"email":    true,
	"google":   true,
	"dingtalk": true,
	"lark":     true,
}

// LoginProviders returns the sign-in allowlist from LOGIN_PROVIDERS
// (comma-separated, e.g. "dingtalk,lark"). nil means unrestricted. The
// legacy LOGIN_DINGTALK_ONLY=true is honored as ["dingtalk"] when
// LOGIN_PROVIDERS is unset so existing deployments keep working while the
// env migrates. Read in two places that must agree: GetConfig (so the login
// UI hides closed entry points) and the router (so closed login routes are
// not registered at all).
func LoginProviders() []string {
	raw := strings.TrimSpace(os.Getenv("LOGIN_PROVIDERS"))
	if raw == "" {
		if strings.EqualFold(strings.TrimSpace(os.Getenv("LOGIN_DINGTALK_ONLY")), "true") {
			return []string{"dingtalk"}
		}
		return nil
	}
	var providers []string
	for _, part := range strings.Split(raw, ",") {
		name := strings.ToLower(strings.TrimSpace(part))
		if knownLoginProviders[name] {
			providers = append(providers, name)
		}
	}
	return providers
}

// LoginProviderAllowed reports whether the named provider may sign in. An
// empty allowlist (no LOGIN_PROVIDERS, no legacy flag) allows everything.
// Exported so the cmd/server router can share the same source of truth.
func LoginProviderAllowed(name string) bool {
	providers := LoginProviders()
	if len(providers) == 0 {
		return true
	}
	for _, p := range providers {
		if p == name {
			return true
		}
	}
	return false
}

func daemonSetupURLsFromEnv() (string, string) {
	serverURL := normalizePublicURL(os.Getenv("MULTICA_PUBLIC_URL"))
	appURL := normalizePublicURL(os.Getenv("MULTICA_APP_URL"))
	if appURL == "" {
		appURL = normalizePublicURL(os.Getenv("FRONTEND_ORIGIN"))
	}
	if appURL == "" {
		return "", ""
	}

	if serverURL == "" {
		serverURL = appURL
	}
	if isOfficialCloudDaemonConfig(appURL) {
		return "", ""
	}
	return serverURL, appURL
}

func normalizePublicURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// isOfficialCloudDaemonConfig reports whether this deployment is the official
// Multica Cloud, identified by its frontend host alone (multica.ai /
// app.multica.ai). The daemon setup for the managed cloud is always
// `multica setup` (which hardcodes api.multica.ai), so the per-deployment URLs
// must be omitted from /api/config even when MULTICA_PUBLIC_URL is unset or
// misconfigured. Previously this also required serverURL==api.multica.ai, so a
// cloud deployment that forgot MULTICA_PUBLIC_URL fell through and emitted a
// `setup self-host --server-url https://multica.ai` command — pointing the
// daemon's backend at the frontend (no /health, no WebSocket proxy).
func isOfficialCloudDaemonConfig(appURL string) bool {
	return urlHostEquals(appURL, "multica.ai") || urlHostEquals(appURL, "app.multica.ai")
}

func urlHostEquals(raw, want string) bool {
	host := canonicalURLHost(raw)
	if host == "" {
		return false
	}
	want = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(want)), ".")
	return host == want
}

func canonicalURLHost(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if host == "" && !strings.Contains(raw, "://") {
		u, err = url.Parse("https://" + raw)
		if err != nil {
			return ""
		}
		host = u.Hostname()
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}
