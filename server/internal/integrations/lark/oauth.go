package lark

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// defaultOAuthAPIBase is the Feishu (CN brand) open API origin. The Lark
// international brand would use https://open.larksuite.com; only feishu is
// wired for now (override via OAuthConfig.APIBase / LARK_OPENAPI_BASE).
const defaultOAuthAPIBase = "https://open.feishu.cn"

// OAuthUser is the identity resolved from a Feishu login code via
// authen/v2/oauth/token + authen/v1/user_info. Field names align with the
// private agent's /internal/lark/oauth/user response.
type OAuthUser struct {
	UnionID   string `json:"union_id"`
	OpenID    string `json:"open_id,omitempty"`
	Name      string `json:"name,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
	Email     string `json:"email,omitempty"`
}

// OAuthClient resolves a one-time Feishu authorization code into a user
// identity. redirectURI must equal the redirect_uri used on the authorize
// redirect — Feishu re-validates it during the token exchange (DingTalk
// does not, Google does).
type OAuthClient interface {
	IsConfigured() bool
	ResolveOAuthUser(ctx context.Context, code, redirectURI string) (OAuthUser, error)
}

type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	APIBase      string
	HTTPClient   *http.Client
	Logger       *slog.Logger
}

// OAuthHTTPClient exchanges the code directly against the Feishu open API.
// Production prefers OAuthAgentClient so the app secret stays inside the
// private channel agent; this direct client is the self-host / local-dev
// fallback (same tiering as the DingTalk integration).
type OAuthHTTPClient struct {
	clientID     string
	clientSecret string
	apiBase      string
	httpClient   *http.Client
	logger       *slog.Logger
}

func NewOAuthHTTPClient(cfg OAuthConfig) *OAuthHTTPClient {
	base := strings.TrimRight(strings.TrimSpace(cfg.APIBase), "/")
	if base == "" {
		base = defaultOAuthAPIBase
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &OAuthHTTPClient{
		clientID:     strings.TrimSpace(cfg.ClientID),
		clientSecret: strings.TrimSpace(cfg.ClientSecret),
		apiBase:      base,
		httpClient:   httpClient,
		logger:       logger,
	}
}

func (c *OAuthHTTPClient) IsConfigured() bool {
	return c != nil && c.clientID != "" && c.clientSecret != ""
}

// oauthTokenResponse covers both shapes of authen/v2/oauth/token: success
// carries access_token; failure is RFC 6749 style ({"error",
// "error_description"}), not the usual Feishu {code,msg} envelope.
type oauthTokenResponse struct {
	AccessToken      string `json:"access_token"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	Code             int    `json:"code"`
}

type oauthUserInfoResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		UnionID         string `json:"union_id"`
		OpenID          string `json:"open_id"`
		Name            string `json:"name"`
		EnName          string `json:"en_name"`
		AvatarURL       string `json:"avatar_url"`
		Email           string `json:"email"`
		EnterpriseEmail string `json:"enterprise_email"`
	} `json:"data"`
}

func (c *OAuthHTTPClient) ResolveOAuthUser(ctx context.Context, code, redirectURI string) (OAuthUser, error) {
	if !c.IsConfigured() {
		return OAuthUser{}, fmt.Errorf("lark oauth: client is not configured")
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return OAuthUser{}, fmt.Errorf("lark oauth: code is required")
	}

	payload, err := json.Marshal(map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     c.clientID,
		"client_secret": c.clientSecret,
		"code":          code,
		"redirect_uri":  strings.TrimSpace(redirectURI),
	})
	if err != nil {
		return OAuthUser{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+"/open-apis/authen/v2/oauth/token", bytes.NewReader(payload))
	if err != nil {
		return OAuthUser{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return OAuthUser{}, fmt.Errorf("lark oauth: token exchange: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))

	var token oauthTokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return OAuthUser{}, fmt.Errorf("lark oauth: decode token response (HTTP %d): %w", res.StatusCode, err)
	}
	if token.AccessToken == "" {
		detail := token.ErrorDescription
		if detail == "" {
			detail = token.Error
		}
		if detail == "" {
			detail = strings.TrimSpace(string(body))
		}
		c.logger.Warn("lark token exchange failed", "status", res.StatusCode)
		return OAuthUser{}, fmt.Errorf("lark oauth: token exchange failed (HTTP %d): %s", res.StatusCode, detail)
	}

	infoReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase+"/open-apis/authen/v1/user_info", nil)
	if err != nil {
		return OAuthUser{}, err
	}
	infoReq.Header.Set("Authorization", "Bearer "+token.AccessToken)

	infoRes, err := c.httpClient.Do(infoReq)
	if err != nil {
		return OAuthUser{}, fmt.Errorf("lark oauth: user_info: %w", err)
	}
	defer infoRes.Body.Close()
	infoBody, _ := io.ReadAll(io.LimitReader(infoRes.Body, 1<<20))

	var info oauthUserInfoResponse
	if err := json.Unmarshal(infoBody, &info); err != nil {
		return OAuthUser{}, fmt.Errorf("lark oauth: decode user_info (HTTP %d): %w", infoRes.StatusCode, err)
	}
	if info.Code != 0 {
		c.logger.Warn("lark user_info failed", "code", info.Code)
		return OAuthUser{}, fmt.Errorf("lark oauth: user_info failed: code=%d msg=%q", info.Code, info.Msg)
	}

	name := strings.TrimSpace(info.Data.Name)
	if name == "" {
		name = strings.TrimSpace(info.Data.EnName)
	}
	email := strings.TrimSpace(info.Data.Email)
	if email == "" {
		email = strings.TrimSpace(info.Data.EnterpriseEmail)
	}
	return OAuthUser{
		UnionID:   strings.TrimSpace(info.Data.UnionID),
		OpenID:    strings.TrimSpace(info.Data.OpenID),
		Name:      name,
		AvatarURL: strings.TrimSpace(info.Data.AvatarURL),
		Email:     email,
	}, nil
}
