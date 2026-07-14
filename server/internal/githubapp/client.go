package githubapp

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	DefaultAPIBase       = "https://api.github.com"
	defaultTimeout       = 20 * time.Second
	defaultResponseLimit = 10 << 20
	tokenRefreshSkew     = time.Minute
	maxRepositoryPages   = 20
)

var ErrUnavailable = errors.New("github app credentials are unavailable")

type Config struct {
	AppID         string
	PrivateKey    string
	APIBase       string
	HTTPClient    *http.Client
	Now           func() time.Time
	ResponseLimit int64
}

type Client struct {
	appID         string
	privateKey    *rsa.PrivateKey
	baseURL       *url.URL
	httpClient    *http.Client
	now           func() time.Time
	responseLimit int64

	mu     sync.Mutex
	tokens map[int64]cachedToken
}

type cachedToken struct {
	value     string
	expiresAt time.Time
}

type Repository struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
}

type TreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

type Tree struct {
	SHA       string      `json:"sha"`
	Truncated bool        `json:"truncated"`
	Entries   []TreeEntry `json:"tree"`
}

type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("github API returned status %d", e.StatusCode)
	}
	return fmt.Sprintf("github API returned status %d: %s", e.StatusCode, e.Message)
}

func New(cfg Config) (*Client, error) {
	appID := strings.TrimSpace(cfg.AppID)
	pemKey := strings.TrimSpace(cfg.PrivateKey)
	if appID == "" || pemKey == "" {
		return nil, ErrUnavailable
	}
	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(pemKey))
	if err != nil {
		return nil, fmt.Errorf("parse GitHub App private key: %w", err)
	}
	base := strings.TrimSpace(cfg.APIBase)
	if base == "" {
		base = DefaultAPIBase
	}
	baseURL, err := url.Parse(base)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, fmt.Errorf("invalid GitHub API base URL")
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/")

	httpClient := cloneHTTPClient(cfg.HTTPClient)
	originalRedirect := httpClient.CheckRedirect
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !sameOrigin(req.URL, baseURL) {
			return errors.New("refusing GitHub API redirect to another origin")
		}
		if originalRedirect != nil {
			return originalRedirect(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}

	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	responseLimit := cfg.ResponseLimit
	if responseLimit <= 0 {
		responseLimit = defaultResponseLimit
	}
	return &Client{
		appID:         appID,
		privateKey:    privateKey,
		baseURL:       baseURL,
		httpClient:    httpClient,
		now:           now,
		responseLimit: responseLimit,
		tokens:        make(map[int64]cachedToken),
	}, nil
}

func cloneHTTPClient(in *http.Client) *http.Client {
	if in == nil {
		return &http.Client{Timeout: defaultTimeout}
	}
	out := *in
	if out.Timeout <= 0 {
		out.Timeout = defaultTimeout
	}
	return &out
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func (c *Client) ListRepositories(ctx context.Context, installationID int64) ([]Repository, error) {
	repositories := make([]Repository, 0)
	for page := 1; page <= maxRepositoryPages; page++ {
		var response struct {
			Repositories []Repository `json:"repositories"`
		}
		path := "/installation/repositories?per_page=100&page=" + strconv.Itoa(page)
		if err := c.doInstallationJSON(ctx, installationID, http.MethodGet, path, nil, &response); err != nil {
			return nil, err
		}
		repositories = append(repositories, response.Repositories...)
		if len(response.Repositories) < 100 {
			return repositories, nil
		}
	}
	return nil, errors.New("github repository pagination limit exceeded")
}

func (c *Client) ResolveCommit(ctx context.Context, installationID int64, owner, repo, ref string) (string, error) {
	var response struct {
		SHA string `json:"sha"`
	}
	path := "/repos/" + escape(owner) + "/" + escape(repo) + "/commits/" + escape(ref)
	if err := c.doInstallationJSON(ctx, installationID, http.MethodGet, path, nil, &response); err != nil {
		return "", err
	}
	if response.SHA == "" {
		return "", errors.New("github commit response did not include a SHA")
	}
	return response.SHA, nil
}

func (c *Client) GetTree(ctx context.Context, installationID int64, owner, repo, sha string) (Tree, error) {
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	commitPath := "/repos/" + escape(owner) + "/" + escape(repo) + "/git/commits/" + escape(sha)
	if err := c.doInstallationJSON(ctx, installationID, http.MethodGet, commitPath, nil, &commit); err != nil {
		return Tree{}, err
	}
	if commit.Tree.SHA == "" {
		return Tree{}, errors.New("github commit response did not include a tree SHA")
	}

	var response Tree
	path := "/repos/" + escape(owner) + "/" + escape(repo) + "/git/trees/" + escape(commit.Tree.SHA) + "?recursive=1"
	if err := c.doInstallationJSON(ctx, installationID, http.MethodGet, path, nil, &response); err != nil {
		return Tree{}, err
	}
	if response.Truncated {
		return Tree{}, errors.New("github tree response was truncated")
	}
	return response, nil
}

func (c *Client) GetBlob(ctx context.Context, installationID int64, owner, repo, sha string) ([]byte, error) {
	var response struct {
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
		Size     int64  `json:"size"`
	}
	path := "/repos/" + escape(owner) + "/" + escape(repo) + "/git/blobs/" + escape(sha)
	if err := c.doInstallationJSON(ctx, installationID, http.MethodGet, path, nil, &response); err != nil {
		return nil, err
	}
	if response.Encoding != "base64" {
		return nil, fmt.Errorf("unsupported GitHub blob encoding %q", response.Encoding)
	}
	content := strings.ReplaceAll(response.Content, "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		return nil, fmt.Errorf("decode GitHub blob: %w", err)
	}
	if int64(len(decoded)) > c.responseLimit {
		return nil, errors.New("github blob exceeds response limit")
	}
	return decoded, nil
}

func escape(value string) string {
	return url.PathEscape(value)
}

func (c *Client) doInstallationJSON(ctx context.Context, installationID int64, method, path string, body any, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		token, err := c.installationToken(ctx, installationID)
		if err != nil {
			return err
		}
		status, err := c.doJSON(ctx, method, path, body, out, token)
		if status == http.StatusUnauthorized && attempt == 0 {
			c.invalidateToken(installationID)
			continue
		}
		return err
	}
	return errors.New("github installation request authentication failed")
}

func (c *Client) installationToken(ctx context.Context, installationID int64) (string, error) {
	now := c.now()
	c.mu.Lock()
	if cached, ok := c.tokens[installationID]; ok && now.Add(tokenRefreshSkew).Before(cached.expiresAt) {
		c.mu.Unlock()
		return cached.value, nil
	}
	c.mu.Unlock()

	appJWT, err := c.signAppJWT(now)
	if err != nil {
		return "", err
	}
	var response struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	path := "/app/installations/" + strconv.FormatInt(installationID, 10) + "/access_tokens"
	status, err := c.doJSON(ctx, http.MethodPost, path, map[string]any{}, &response, appJWT)
	if err != nil {
		return "", err
	}
	if status != http.StatusCreated || response.Token == "" || response.ExpiresAt.IsZero() {
		return "", errors.New("github installation token response was incomplete")
	}
	c.mu.Lock()
	c.tokens[installationID] = cachedToken{value: response.Token, expiresAt: response.ExpiresAt}
	c.mu.Unlock()
	return response.Token, nil
}

func (c *Client) invalidateToken(installationID int64) {
	c.mu.Lock()
	delete(c.tokens, installationID)
	c.mu.Unlock()
}

func (c *Client) signAppJWT(now time.Time) (string, error) {
	claims := jwt.MapClaims{
		"iat": now.Add(-time.Minute).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": c.appID,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(c.privateKey)
	if err != nil {
		return "", fmt.Errorf("sign GitHub App JWT: %w", err)
	}
	return signed, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, body any, out any, bearer string) (int, error) {
	endpoint, err := c.endpoint(path)
	if err != nil {
		return 0, err
	}
	var bodyReader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		bodyReader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), bodyReader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "multica-agent-source")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("github API request failed: %w", err)
	}
	defer resp.Body.Close()
	payload, err := readLimited(resp.Body, c.responseLimit)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiResponse struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(payload, &apiResponse)
		return resp.StatusCode, &APIError{StatusCode: resp.StatusCode, Message: apiResponse.Message}
	}
	if out != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode GitHub API response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

func (c *Client) endpoint(path string) (*url.URL, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, errors.New("invalid GitHub API path")
	}
	endpoint, err := url.Parse(strings.TrimRight(c.baseURL.String(), "/") + path)
	if err != nil || !sameOrigin(endpoint, c.baseURL) {
		return nil, errors.New("invalid GitHub API endpoint")
	}
	return endpoint, nil
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > limit {
		return nil, errors.New("github API response exceeds limit")
	}
	return payload, nil
}
