package dws

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Env selects the DWS environment: the MCP gateway and the OAuth endpoints.
type Env string

const (
	EnvProduction Env = "production"
	EnvStaging    Env = "staging"
)

// Server is a DWS MCP product server.
type Server string

const (
	ServerChat    Server = "chat"
	ServerIM      Server = "im"
	ServerContact Server = "contact"
)

// serverIDs mirrors dingtalk-workspace-cli internal/syncdata/endpoints.go.
// The staging gateway serves the same IDs.
var serverIDs = map[Server]string{
	ServerChat:    "0a1609437385696b77fc4771c3ddaf5656b487f809966c0cc8d4755e7b1d3b74",
	ServerIM:      "450eede6b54d83e030140e66ec77c98a2e89a0869ef4db481f8217a98a42f821",
	ServerContact: "db4b26cb38ea6a8739ad55d1997fa1da608cd36b33a6cf0f77884f70c49382fe",
}

// DingTalkUserAccessTokenURL is DingTalk's OAuth token endpoint, used when a
// client secret is configured.
const DingTalkUserAccessTokenURL = "https://api.dingtalk.com/v1.0/oauth2/userAccessToken"

// Config configures a Client. The zero value talks to production and uses
// the DWS-hosted code exchange, which needs no client secret.
type Config struct {
	// Env selects gateway and OAuth endpoints; empty means production.
	Env Env
	// ClientSecret switches code exchange and refresh to DingTalk OAuth with
	// the app secret. Empty uses the DWS-hosted exchange (/oauth2/getToken).
	ClientSecret string
	// SkipVerify skips the identity check at the end of New.
	SkipVerify bool
	// RefreshSkew refreshes the token this long before it expires (default 5m).
	RefreshSkew time.Duration
	// HTTP defaults to a client with a 30s timeout. Redirects are never
	// followed, whatever its CheckRedirect says.
	HTTP *http.Client
	// Header is added to every tool call, e.g. x-dingtalk-trace-id.
	Header http.Header

	// Endpoint overrides for tests and private deployments.
	GatewayURL string // default https://{pre-}mcp-gw.dingtalk.com
	AuthURL    string // DWS-hosted OAuth, default https://{pre-}mcp.dingtalk.com
	TokenURL   string // DingTalk OAuth, default DingTalkUserAccessTokenURL

	Now func() time.Time
}

func (c Config) validate() error {
	switch c.Env {
	case "", EnvProduction, EnvStaging:
	default:
		return fmt.Errorf("unknown env %q", c.Env)
	}
	for name, raw := range map[string]string{"GatewayURL": c.GatewayURL, "AuthURL": c.AuthURL, "TokenURL": c.TokenURL} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("%s is not an http(s) URL", name)
		}
	}
	if strings.ContainsAny(c.ClientSecret, " \t\r\n") {
		return fmt.Errorf("ClientSecret contains whitespace")
	}
	return nil
}

func (c Config) gatewayURL() string {
	if c.GatewayURL != "" {
		return strings.TrimRight(c.GatewayURL, "/")
	}
	if c.Env == EnvStaging {
		return "https://pre-mcp-gw.dingtalk.com"
	}
	return "https://mcp-gw.dingtalk.com"
}

func (c Config) authURL() string {
	if c.AuthURL != "" {
		return strings.TrimRight(c.AuthURL, "/")
	}
	if c.Env == EnvStaging {
		return "https://pre-mcp.dingtalk.com"
	}
	return "https://mcp.dingtalk.com"
}

func (c Config) tokenURL() string {
	if c.TokenURL != "" {
		return c.TokenURL
	}
	return DingTalkUserAccessTokenURL
}

func (c Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Config) skew() time.Duration {
	if c.RefreshSkew > 0 {
		return c.RefreshSkew
	}
	return 5 * time.Minute
}

var defaultHTTPClient = &http.Client{Timeout: 30 * time.Second}

// httpClient returns a copy of the configured client that never follows a
// redirect: net/http re-sends custom headers such as x-user-access-token to
// a redirect target on any host, and replays a POST body (a client secret or
// refresh token) on 307/308.
func (c Config) httpClient() *http.Client {
	base := c.HTTP
	if base == nil {
		base = defaultHTTPClient
	}
	copied := *base
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copied
}

func serverPath(server Server) (string, error) {
	id, ok := serverIDs[server]
	if !ok {
		return "", fmt.Errorf("dws: unknown server %q", server)
	}
	return "/server/" + id, nil
}

// String describes the Config without its client secret or headers.
func (c Config) String() string {
	env := c.Env
	if env == "" {
		env = EnvProduction
	}
	secret := "none"
	if c.ClientSecret != "" {
		secret = "[redacted]"
	}
	return fmt.Sprintf("dws.Config{env=%s, clientSecret=%s, gateway=%s}", env, secret, c.gatewayURL())
}

// GoString keeps %#v as safe as %v.
func (c Config) GoString() string { return c.String() }
