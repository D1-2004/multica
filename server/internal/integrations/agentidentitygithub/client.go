package agentidentitygithub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultTimeout = 10 * time.Second

type Config struct {
	BaseURL    string
	Timeout    time.Duration
	HTTPClient *http.Client
}

type Client struct {
	baseURL    string
	timeout    time.Duration
	httpClient *http.Client
}

func NewClient(cfg Config) *Client {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL:    baseURL,
		timeout:    timeout,
		httpClient: httpClient,
	}
}

func (c *Client) Enabled() bool {
	return c != nil && c.baseURL != ""
}

type OAuthStartRequest struct {
	WorkspaceID string `json:"workspaceId"`
	AgentID     string `json:"agentId"`
	UserID      string `json:"userId"`
	ReturnURL   string `json:"returnUrl,omitempty"`
}

type OAuthStartResponse struct {
	OK               bool   `json:"ok"`
	State            string `json:"state,omitempty"`
	AuthorizationURL string `json:"authorizationUrl,omitempty"`
	ErrorCode        string `json:"errorCode,omitempty"`
	ErrorMessage     string `json:"errorMessage,omitempty"`
}

type Connection struct {
	OK               bool   `json:"ok"`
	ConnectionID     string `json:"connectionId,omitempty"`
	AccountLogin     string `json:"accountLogin,omitempty"`
	AccountID        string `json:"accountId,omitempty"`
	Status           string `json:"status,omitempty"`
	GrantedScopes    string `json:"grantedScopes,omitempty"`
	AccessExpiresAt  *int64 `json:"accessExpiresAt,omitempty"`
	RefreshExpiresAt *int64 `json:"refreshExpiresAt,omitempty"`
	LastRefreshAt    *int64 `json:"lastRefreshAt,omitempty"`
	LastTestAt       *int64 `json:"lastTestAt,omitempty"`
	ErrorCode        string `json:"errorCode,omitempty"`
	ErrorMessage     string `json:"errorMessage,omitempty"`
}

type TestResult struct {
	OK            bool   `json:"ok"`
	Refreshed     bool   `json:"refreshed"`
	ConnectionID  string `json:"connectionId,omitempty"`
	AccountLogin  string `json:"accountLogin,omitempty"`
	AccountID     string `json:"accountId,omitempty"`
	GrantedScopes string `json:"grantedScopes,omitempty"`
	ErrorCode     string `json:"errorCode,omitempty"`
	ErrorMessage  string `json:"errorMessage,omitempty"`
}

type ServiceError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *ServiceError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return fmt.Sprintf("agent identity github request failed: status %d", e.StatusCode)
}

func (c *Client) StartOAuth(ctx context.Context, req OAuthStartRequest) (OAuthStartResponse, error) {
	query := url.Values{}
	query.Set("workspaceId", req.WorkspaceID)
	query.Set("agentId", req.AgentID)
	query.Set("userId", req.UserID)
	if req.ReturnURL != "" {
		query.Set("returnUrl", req.ReturnURL)
	}
	var out OAuthStartResponse
	err := c.doJSON(ctx, http.MethodGet, "/api/agent-identity/v1/connections/github/oauth/start", query, nil, &out)
	return out, err
}

func (c *Client) GetStatus(ctx context.Context, workspaceID, agentID, userID string) (Connection, error) {
	query := url.Values{}
	query.Set("workspaceId", workspaceID)
	query.Set("agentId", agentID)
	query.Set("userId", userID)
	var out Connection
	err := c.doJSON(ctx, http.MethodGet, "/api/agent-identity/v1/connections/github/status", query, nil, &out)
	return out, err
}

func (c *Client) TestConnection(ctx context.Context, connectionID string) (TestResult, error) {
	path := "/api/agent-identity/v1/connections/github/" + url.PathEscape(connectionID) + "/test"
	var out TestResult
	err := c.doJSON(ctx, http.MethodPost, path, nil, nil, &out)
	return out, err
}

func (c *Client) doJSON(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	if !c.Enabled() {
		return &ServiceError{StatusCode: http.StatusServiceUnavailable, Code: "NOT_CONFIGURED", Message: "agent identity github is not configured"}
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}

	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
	httpReq.Header.Set("Accept", "application/json")
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody struct {
			ErrorCode    string `json:"errorCode"`
			ErrorMessage string `json:"errorMessage"`
			OK           bool   `json:"ok"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		return &ServiceError{
			StatusCode: resp.StatusCode,
			Code:       errBody.ErrorCode,
			Message:    errBody.ErrorMessage,
		}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
