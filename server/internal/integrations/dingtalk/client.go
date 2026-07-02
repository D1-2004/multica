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
	"sync"
	"time"
)

const (
	defaultOpenAPIBase = "https://api.dingtalk.com"
	defaultOAPIBase    = "https://oapi.dingtalk.com"
)

type Config struct {
	AppKey      string
	AppSecret   string
	OpenAPIBase string
	OAPIBase    string
	HTTPClient  *http.Client
	Logger      *slog.Logger
}

type Client struct {
	appKey      string
	appSecret   string
	openAPIBase string
	oapiBase    string
	httpClient  *http.Client
	logger      *slog.Logger

	tokenMu sync.Mutex
	token   tokenCache
}

type tokenCache struct {
	value     string
	expiresAt time.Time
}

type User struct {
	UserID     string  `json:"user_id"`
	UnionID    string  `json:"union_id,omitempty"`
	Name       string  `json:"name"`
	AvatarURL  string  `json:"avatar_url,omitempty"`
	Mobile     string  `json:"mobile,omitempty"`
	Title      string  `json:"title,omitempty"`
	Email      string  `json:"email,omitempty"`
	Department []int64 `json:"department_ids,omitempty"`
}

type CapabilityClient interface {
	IsConfigured() bool
	SearchUsers(ctx context.Context, query string, limit int) ([]User, error)
	AddGroupMembers(ctx context.Context, chatID string, userIDs []string) error
}

type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("dingtalk api error: HTTP %d %s %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("dingtalk api error: %s %s", e.Code, e.Message)
}

func NewClient(cfg Config) *Client {
	openBase := strings.TrimRight(strings.TrimSpace(cfg.OpenAPIBase), "/")
	if openBase == "" {
		openBase = defaultOpenAPIBase
	}
	oapiBase := strings.TrimRight(strings.TrimSpace(cfg.OAPIBase), "/")
	if oapiBase == "" {
		oapiBase = defaultOAPIBase
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		appKey:      strings.TrimSpace(cfg.AppKey),
		appSecret:   strings.TrimSpace(cfg.AppSecret),
		openAPIBase: openBase,
		oapiBase:    oapiBase,
		httpClient:  httpClient,
		logger:      logger,
	}
}

func (c *Client) IsConfigured() bool {
	return c != nil && c.appKey != "" && c.appSecret != ""
}

func (c *Client) SearchUsers(ctx context.Context, query string, limit int) ([]User, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 20 {
		limit = 10
	}

	if looksLikeMobile(query) {
		userID, err := c.GetUserIDByMobile(ctx, query)
		if err != nil {
			return nil, err
		}
		u, err := c.GetUser(ctx, userID)
		if err != nil {
			return nil, err
		}
		return []User{u}, nil
	}

	userIDs, err := c.searchUserIDs(ctx, query, limit)
	if err != nil {
		if users, deptErr := c.searchDepartmentUsers(ctx, query, limit); deptErr == nil && len(users) > 0 {
			return users, nil
		}
		// If the deployment has not yet published enterprise address-book
		// search, still support exact userId entry.
		if u, detailErr := c.GetUser(ctx, query); detailErr == nil {
			return []User{u}, nil
		}
		return nil, err
	}

	users := make([]User, 0, len(userIDs))
	for _, userID := range userIDs {
		u, err := c.GetUser(ctx, userID)
		if err != nil {
			c.logger.Warn("dingtalk: enrich searched user failed", "user_id", userID, "error", err)
			users = append(users, User{UserID: userID, Name: userID})
			continue
		}
		users = append(users, u)
	}
	return users, nil
}

func (c *Client) searchUserIDs(ctx context.Context, query string, limit int) ([]string, error) {
	token, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	var resp struct {
		List []string `json:"list"`
	}
	if err := c.postOpenAPI(ctx, "/v1.0/contact/users/search", token, map[string]any{
		"queryWord": query,
		"offset":    0,
		"size":      limit,
	}, &resp); err != nil {
		return nil, err
	}
	return resp.List, nil
}

type departmentUserBrief struct {
	UserID  string `json:"userid"`
	UnionID string `json:"unionid"`
	Name    string `json:"name"`
}

func (c *Client) searchDepartmentUsers(ctx context.Context, query string, limit int) ([]User, error) {
	queue := []int64{1}
	visited := make(map[int64]struct{})
	matches := make([]User, 0, limit)
	var firstErr error

	for len(queue) > 0 && len(matches) < limit && len(visited) < 200 {
		deptID := queue[0]
		queue = queue[1:]
		if _, ok := visited[deptID]; ok {
			continue
		}
		visited[deptID] = struct{}{}

		if subIDs, err := c.listSubDepartmentIDs(ctx, deptID); err == nil {
			for _, subID := range subIDs {
				if _, ok := visited[subID]; !ok {
					queue = append(queue, subID)
				}
			}
		} else if firstErr == nil {
			firstErr = err
		}

		briefs, err := c.listDepartmentUsers(ctx, deptID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, brief := range briefs {
			if !matchesDingTalkUserBrief(brief, query) {
				continue
			}
			user, err := c.GetUser(ctx, brief.UserID)
			if err != nil {
				c.logger.Warn("dingtalk: enrich department user failed", "user_id", brief.UserID, "error", err)
				user = User{UserID: brief.UserID, UnionID: brief.UnionID, Name: brief.Name}
			}
			matches = append(matches, user)
			if len(matches) >= limit {
				break
			}
		}
	}
	if len(matches) > 0 {
		return matches, nil
	}
	return nil, firstErr
}

func (c *Client) listSubDepartmentIDs(ctx context.Context, deptID int64) ([]int64, error) {
	var result struct {
		DeptIDList []int64 `json:"dept_id_list"`
	}
	if err := c.postLegacy(ctx, "/topapi/v2/department/listsubid", map[string]any{
		"dept_id": deptID,
	}, &result); err != nil {
		return nil, err
	}
	return result.DeptIDList, nil
}

func (c *Client) listDepartmentUsers(ctx context.Context, deptID int64) ([]departmentUserBrief, error) {
	const pageSize = 100
	var users []departmentUserBrief
	var cursor int64
	for pages := 0; pages < 20; pages++ {
		var result struct {
			HasMore    bool                  `json:"has_more"`
			NextCursor int64                 `json:"next_cursor"`
			List       []departmentUserBrief `json:"list"`
		}
		if err := c.postLegacy(ctx, "/topapi/user/listsimple", map[string]any{
			"dept_id":  deptID,
			"cursor":   cursor,
			"size":     pageSize,
			"language": "zh_CN",
		}, &result); err != nil {
			return nil, err
		}
		users = append(users, result.List...)
		if !result.HasMore {
			break
		}
		cursor = result.NextCursor
	}
	return users, nil
}

func (c *Client) GetUserIDByMobile(ctx context.Context, mobile string) (string, error) {
	var result struct {
		UserID string `json:"userid"`
	}
	if err := c.postLegacy(ctx, "/topapi/v2/user/getbymobile", map[string]any{
		"mobile": strings.TrimSpace(mobile),
	}, &result); err != nil {
		return "", err
	}
	if result.UserID == "" {
		return "", &APIError{Code: "empty_userid", Message: "DingTalk returned no userId"}
	}
	return result.UserID, nil
}

func (c *Client) GetUser(ctx context.Context, userID string) (User, error) {
	var raw struct {
		UserID     string  `json:"userid"`
		UnionID    string  `json:"unionid"`
		Name       string  `json:"name"`
		Avatar     string  `json:"avatar"`
		Mobile     string  `json:"mobile"`
		Title      string  `json:"title"`
		Email      string  `json:"email"`
		OrgEmail   string  `json:"org_email"`
		DeptIDList []int64 `json:"dept_id_list"`
	}
	if err := c.postLegacy(ctx, "/topapi/v2/user/get", map[string]any{
		"userid":   strings.TrimSpace(userID),
		"language": "zh_CN",
	}, &raw); err != nil {
		return User{}, err
	}
	if raw.UserID == "" {
		raw.UserID = strings.TrimSpace(userID)
	}
	name := strings.TrimSpace(raw.Name)
	if name == "" {
		name = raw.UserID
	}
	email := strings.TrimSpace(raw.Email)
	if email == "" {
		email = strings.TrimSpace(raw.OrgEmail)
	}
	return User{
		UserID:     raw.UserID,
		UnionID:    raw.UnionID,
		Name:       name,
		AvatarURL:  raw.Avatar,
		Mobile:     raw.Mobile,
		Title:      raw.Title,
		Email:      email,
		Department: raw.DeptIDList,
	}, nil
}

func (c *Client) AddGroupMembers(ctx context.Context, chatID string, userIDs []string) error {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return &APIError{Code: "missing_chat_id", Message: "DingTalk chatid is required"}
	}
	cleaned := make([]string, 0, len(userIDs))
	for _, userID := range userIDs {
		userID = strings.TrimSpace(userID)
		if userID != "" {
			cleaned = append(cleaned, userID)
		}
	}
	if len(cleaned) == 0 {
		return &APIError{Code: "missing_user_ids", Message: "at least one DingTalk userId is required"}
	}
	return c.postLegacy(ctx, "/chat/update", map[string]any{
		"chatid":         chatID,
		"add_useridlist": cleaned,
	}, nil)
}

func (c *Client) postOpenAPI(ctx context.Context, path, token string, body any, out any) error {
	req, err := c.newJSONRequest(ctx, http.MethodPost, c.openAPIBase+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("x-acs-dingtalk-access-token", token)
	return c.doJSON(req, out, false)
}

func (c *Client) postLegacy(ctx context.Context, path string, body any, resultOut any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	endpoint := c.oapiBase + path + "?access_token=" + url.QueryEscape(token)
	var envelope legacyEnvelope
	if err := c.postRaw(ctx, endpoint, body, &envelope); err != nil {
		return err
	}
	if !envelope.OK() {
		return &APIError{Code: envelope.CodeString(), Message: envelope.ErrMsg}
	}
	if resultOut == nil || len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return nil
	}
	return json.Unmarshal(envelope.Result, resultOut)
}

func (c *Client) postRaw(ctx context.Context, endpoint string, body any, out any) error {
	req, err := c.newJSONRequest(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return err
	}
	return c.doJSON(req, out, false)
}

func (c *Client) newJSONRequest(ctx context.Context, method, endpoint string, body any) (*http.Request, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (c *Client) doJSON(req *http.Request, out any, tokenRequest bool) error {
	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return &APIError{Status: res.StatusCode, Code: "http_error", Message: strings.TrimSpace(string(body))}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode dingtalk response: %w", err)
	}
	if tokenRequest {
		return nil
	}
	return nil
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()

	if c.token.value != "" && time.Until(c.token.expiresAt) > time.Minute {
		return c.token.value, nil
	}
	var resp struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int64  `json:"expireIn"`
	}
	if err := c.postRaw(ctx, c.openAPIBase+"/v1.0/oauth2/accessToken", map[string]string{
		"appKey":    c.appKey,
		"appSecret": c.appSecret,
	}, &resp); err != nil {
		return "", err
	}
	if resp.AccessToken == "" {
		return "", &APIError{Code: "empty_access_token", Message: "DingTalk returned no access token"}
	}
	ttl := resp.ExpireIn
	if ttl <= 0 {
		ttl = 7200
	}
	c.token = tokenCache{value: resp.AccessToken, expiresAt: time.Now().Add(time.Duration(ttl) * time.Second)}
	return resp.AccessToken, nil
}

type legacyEnvelope struct {
	ErrCode json.RawMessage `json:"errcode"`
	ErrMsg  string          `json:"errmsg"`
	Result  json.RawMessage `json:"result"`
}

func (e legacyEnvelope) OK() bool {
	return e.CodeString() == "0" || e.CodeString() == ""
}

func (e legacyEnvelope) CodeString() string {
	raw := strings.TrimSpace(string(e.ErrCode))
	if raw == "" || raw == "null" {
		return ""
	}
	raw = strings.Trim(raw, `"`)
	return raw
}

func looksLikeMobile(value string) bool {
	digits := 0
	for _, r := range value {
		if r >= '0' && r <= '9' {
			digits++
			continue
		}
		if r != '+' && r != '-' && r != ' ' {
			return false
		}
	}
	return digits >= 7
}

func matchesDingTalkUserBrief(user departmentUserBrief, query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return false
	}
	fields := []string{user.UserID, user.UnionID, user.Name}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}
