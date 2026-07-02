package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type AgentClientConfig struct {
	BaseURL        string
	InternalSecret string
	HTTPClient     *http.Client
	Logger         *slog.Logger
}

type AgentClient struct {
	baseURL        string
	internalSecret string
	httpClient     *http.Client
	logger         *slog.Logger
}

func NewAgentClient(cfg AgentClientConfig) *AgentClient {
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &AgentClient{
		baseURL:        strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		internalSecret: strings.TrimSpace(cfg.InternalSecret),
		httpClient:     httpClient,
		logger:         logger,
	}
}

func (c *AgentClient) IsConfigured() bool {
	return c != nil && c.baseURL != "" && c.internalSecret != ""
}

func (c *AgentClient) SearchUsers(ctx context.Context, query string, limit int) ([]User, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 20 {
		limit = 10
	}

	values := url.Values{}
	values.Set("q", query)
	values.Set("limit", fmt.Sprintf("%d", limit))
	var resp struct {
		Users []User `json:"users"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/internal/dingtalk/users/search?"+values.Encode(), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Users, nil
}

func (c *AgentClient) AddGroupMembers(ctx context.Context, chatID string, userIDs []string) error {
	cleaned := make([]string, 0, len(userIDs))
	for _, userID := range userIDs {
		userID = strings.TrimSpace(userID)
		if userID != "" {
			cleaned = append(cleaned, userID)
		}
	}
	var resp struct {
		AddedUserIDs []string `json:"added_user_ids"`
	}
	return c.doJSON(ctx, http.MethodPost, "/internal/dingtalk/group-members", map[string]any{
		"chat_id":  strings.TrimSpace(chatID),
		"user_ids": cleaned,
	}, &resp)
}

func (c *AgentClient) ResolveOAuthUser(ctx context.Context, code string) (OAuthUser, error) {
	var resp struct {
		User OAuthUser `json:"user"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/internal/dingtalk/oauth/user", map[string]any{
		"code": strings.TrimSpace(code),
	}, &resp); err != nil {
		return OAuthUser{}, err
	}
	if strings.TrimSpace(resp.User.UnionID) == "" {
		return OAuthUser{}, &APIError{Code: "missing_union_id", Message: "DingTalk agent returned no unionId"}
	}
	return resp.User, nil
}

func (c *AgentClient) SendPersonalNotification(ctx context.Context, req PersonalNotification) error {
	req.Card.Title = strings.TrimSpace(req.Card.Title)
	req.Card.Text = strings.TrimSpace(req.Card.Text)
	req.Card.URL = strings.TrimSpace(req.Card.URL)
	req.Card.ButtonText = strings.TrimSpace(req.Card.ButtonText)
	req.Recipient.UserID = strings.TrimSpace(req.Recipient.UserID)
	req.Recipient.UnionID = strings.TrimSpace(req.Recipient.UnionID)
	req.Recipient.Email = strings.TrimSpace(req.Recipient.Email)
	req.Recipient.Name = strings.TrimSpace(req.Recipient.Name)
	req.Source = strings.TrimSpace(req.Source)
	req.DedupeKey = strings.TrimSpace(req.DedupeKey)
	if req.Card.Title == "" {
		return &APIError{Code: "missing_title", Message: "DingTalk notification title is required"}
	}
	if req.Card.Text == "" {
		return &APIError{Code: "missing_text", Message: "DingTalk notification text is required"}
	}
	return c.doJSON(ctx, http.MethodPost, "/internal/dingtalk/notifications/personal", req, nil)
}

func (c *AgentClient) doJSON(ctx context.Context, method, path string, body any, out any) error {
	if !c.IsConfigured() {
		return &APIError{Code: "agent_not_configured", Message: "DingTalk agent client is not configured"}
	}

	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("x-internal-secret", c.internalSecret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	resBody, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		c.logger.Warn("dingtalk agent request failed", "method", method, "path", path, "status", res.StatusCode)
		return &APIError{Status: res.StatusCode, Code: "agent_http_error", Message: strings.TrimSpace(string(resBody))}
	}
	if out == nil || len(resBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(resBody, out); err != nil {
		return fmt.Errorf("decode dingtalk agent response: %w", err)
	}
	return nil
}
