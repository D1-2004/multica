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

type OAuthAgentClientConfig struct {
	BaseURL        string
	InternalSecret string
	HTTPClient     *http.Client
	Logger         *slog.Logger
}

// OAuthAgentClient resolves Feishu login codes through the private channel
// agent (the dingtalk-native-agent service) so the Feishu app secret never
// enters this backend's environment — the same pattern as
// dingtalk.AgentClient (docs/dingtalk-private-agent-integration-plan.md).
type OAuthAgentClient struct {
	baseURL        string
	internalSecret string
	httpClient     *http.Client
	logger         *slog.Logger
}

func NewOAuthAgentClient(cfg OAuthAgentClientConfig) *OAuthAgentClient {
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &OAuthAgentClient{
		baseURL:        strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		internalSecret: strings.TrimSpace(cfg.InternalSecret),
		httpClient:     httpClient,
		logger:         logger,
	}
}

func (c *OAuthAgentClient) IsConfigured() bool {
	return c != nil && c.baseURL != "" && c.internalSecret != ""
}

func (c *OAuthAgentClient) ResolveOAuthUser(ctx context.Context, code, redirectURI string) (OAuthUser, error) {
	if !c.IsConfigured() {
		return OAuthUser{}, fmt.Errorf("lark oauth agent: client is not configured")
	}
	payload, err := json.Marshal(map[string]string{
		"code":         strings.TrimSpace(code),
		"redirect_uri": strings.TrimSpace(redirectURI),
	})
	if err != nil {
		return OAuthUser{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/internal/lark/oauth/user", bytes.NewReader(payload))
	if err != nil {
		return OAuthUser{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-internal-secret", c.internalSecret)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return OAuthUser{}, fmt.Errorf("lark oauth agent: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		c.logger.Warn("lark agent request failed", "status", res.StatusCode)
		return OAuthUser{}, fmt.Errorf("lark oauth agent: HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	var resp struct {
		User OAuthUser `json:"user"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return OAuthUser{}, fmt.Errorf("lark oauth agent: decode response: %w", err)
	}
	if strings.TrimSpace(resp.User.UnionID) == "" {
		return OAuthUser{}, fmt.Errorf("lark oauth agent: agent returned no union_id")
	}
	return resp.User, nil
}
