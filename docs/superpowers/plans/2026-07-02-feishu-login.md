# Feishu (Lark) Login Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add "Sign in with Feishu" to Multica web + desktop, mirroring the DingTalk login end to end, with code exchange running agent-preferred (dingtalk-native-agent) and direct-client fallback.

**Architecture:** Browser → `accounts.feishu.cn/open-apis/authen/v1/authorize` (state stamped `provider:lark`) → shared `/auth/callback` → `POST /auth/lark {code, redirect_uri}` → Go handler resolves identity via `h.LarkOAuth` (private agent `POST /internal/lark/oauth/user`, or direct Feishu API) → synthetic email `<union_id>@feishu.cn` into the existing email-keyed user model. Spec: `docs/superpowers/specs/2026-07-02-feishu-login-design.md`.

**Tech Stack:** Go (chi, net/http, httptest), TypeScript (Next.js, zod, zustand, vitest), Fastify (dingtalk-native-agent, separate repo at `/Users/yuanzhan/d1/dingtalk-native-agent`).

**Verified endpoint contracts** (probed live on 2026-07-02):
- `POST {base}/open-apis/authen/v2/oauth/token` body `{grant_type:"authorization_code", client_id, client_secret, code, redirect_uri}`. Failure body is RFC 6749 style `{"error":"invalid_request","error_description":"...","code":20063}` (HTTP 4xx) — NOT the Feishu `{code,msg}` envelope. Success carries `access_token`.
- `GET {base}/open-apis/authen/v1/user_info` with `Authorization: Bearer <user_access_token>` → `{"code":0,"msg":"success","data":{union_id, open_id, name, en_name, avatar_url, email?, enterprise_email?}}`.
- `base` = `https://open.feishu.cn` (feishu brand only for now).

**Repo note:** Tasks 1–6 are in the multica repo (branch `develop`). Task 7 is in `/Users/yuanzhan/d1/dingtalk-native-agent` (its own git repo — commit there separately). Go handler tests need the local Postgres from `make dev`; they self-skip when the DB is unreachable.

---

### Task 1: Go — Lark OAuth types + direct HTTP client

**Files:**
- Create: `server/internal/integrations/lark/oauth.go`
- Test: `server/internal/integrations/lark/oauth_test.go`

The existing `lark` package is the bot integration (tenant tokens, IM). User-login OAuth is new. No name collisions: `OAuthUser`, `OAuthClient`, `OAuthConfig`, `OAuthHTTPClient`, `NewOAuthHTTPClient`, `defaultOAuthAPIBase` are all unused in the package today (verified by grep).

- [ ] **Step 1: Write the failing test**

Create `server/internal/integrations/lark/oauth_test.go`:

```go
package lark

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newOAuthFixture serves both authen endpoints. tokenStatus/tokenBody control
// the token exchange response; infoBody controls user_info.
func newOAuthFixture(t *testing.T, tokenStatus int, tokenBody string, infoBody string) (*httptest.Server, *OAuthHTTPClient) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/authen/v2/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("token endpoint method = %s, want POST", r.Method)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("token endpoint body decode: %v", err)
		}
		if body["grant_type"] != "authorization_code" {
			t.Errorf("grant_type = %q, want authorization_code", body["grant_type"])
		}
		if body["client_id"] != "cli_test" || body["client_secret"] != "secret_test" {
			t.Errorf("credentials not forwarded: %v", body)
		}
		if body["redirect_uri"] != "http://localhost:3000/auth/callback" {
			t.Errorf("redirect_uri = %q", body["redirect_uri"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(tokenStatus)
		_, _ = w.Write([]byte(tokenBody))
	})
	mux.HandleFunc("/open-apis/authen/v1/user_info", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer u-token-123" {
			t.Errorf("user_info Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(infoBody))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := NewOAuthHTTPClient(OAuthConfig{
		ClientID:     "cli_test",
		ClientSecret: "secret_test",
		APIBase:      srv.URL,
	})
	return srv, client
}

func TestOAuthHTTPClientResolvesUser(t *testing.T) {
	_, client := newOAuthFixture(t,
		http.StatusOK,
		`{"code":0,"access_token":"u-token-123","expires_in":7199}`,
		`{"code":0,"msg":"success","data":{"union_id":"on_abc","open_id":"ou_xyz","name":"张三","en_name":"San Zhang","avatar_url":"https://p.example/a.png","enterprise_email":"san@corp.example"}}`,
	)
	user, err := client.ResolveOAuthUser(context.Background(), "authcode", "http://localhost:3000/auth/callback")
	if err != nil {
		t.Fatalf("ResolveOAuthUser: %v", err)
	}
	if user.UnionID != "on_abc" || user.OpenID != "ou_xyz" || user.Name != "张三" {
		t.Errorf("unexpected user: %+v", user)
	}
	if user.AvatarURL != "https://p.example/a.png" || user.Email != "san@corp.example" {
		t.Errorf("unexpected profile fields: %+v", user)
	}
}

func TestOAuthHTTPClientSurfacesRFC6749Error(t *testing.T) {
	// Feishu's v2 token endpoint answers failures in RFC 6749 shape, not the
	// {code,msg} envelope — the client must surface error_description.
	_, client := newOAuthFixture(t,
		http.StatusBadRequest,
		`{"error":"invalid_grant","error_description":"code is invalid or expired","code":20050}`,
		`{}`,
	)
	_, err := client.ResolveOAuthUser(context.Background(), "badcode", "http://localhost:3000/auth/callback")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "code is invalid or expired") {
		t.Errorf("error should carry error_description, got: %v", err)
	}
}

func TestOAuthHTTPClientSurfacesUserInfoEnvelopeError(t *testing.T) {
	_, client := newOAuthFixture(t,
		http.StatusOK,
		`{"code":0,"access_token":"u-token-123"}`,
		`{"code":20005,"msg":"user_access_token invalid"}`,
	)
	_, err := client.ResolveOAuthUser(context.Background(), "authcode", "http://localhost:3000/auth/callback")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "20005") {
		t.Errorf("error should carry envelope code, got: %v", err)
	}
}

func TestOAuthHTTPClientIsConfigured(t *testing.T) {
	if NewOAuthHTTPClient(OAuthConfig{}).IsConfigured() {
		t.Error("empty config should not be configured")
	}
	if !NewOAuthHTTPClient(OAuthConfig{ClientID: "cli_x", ClientSecret: "s"}).IsConfigured() {
		t.Error("id+secret should be configured")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/yuanzhan/Documents/multica-railway/multica/server && go test ./internal/integrations/lark/ -run 'OAuthHTTP' -v`
Expected: FAIL (compile error: `undefined: OAuthHTTPClient`, `OAuthConfig`, `NewOAuthHTTPClient`)

- [ ] **Step 3: Write the implementation**

Create `server/internal/integrations/lark/oauth.go`:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/yuanzhan/Documents/multica-railway/multica/server && gofmt -l ./internal/integrations/lark/ && go vet ./internal/integrations/lark/ && go test ./internal/integrations/lark/ -run 'OAuthHTTP' -v`
Expected: PASS (4 tests), gofmt prints nothing.

- [ ] **Step 5: Commit**

```bash
cd /Users/yuanzhan/Documents/multica-railway/multica
git add server/internal/integrations/lark/oauth.go server/internal/integrations/lark/oauth_test.go
git commit -m "feat(lark): add Feishu user OAuth direct client (authen v2 token + user_info)"
```

---

### Task 2: Go — Lark OAuth private-agent client

**Files:**
- Create: `server/internal/integrations/lark/oauth_agent_client.go`
- Test: `server/internal/integrations/lark/oauth_agent_client_test.go`

Mirrors `server/internal/integrations/dingtalk/agent_client.go`, but the body carries `redirect_uri` too.

- [ ] **Step 1: Write the failing test**

Create `server/internal/integrations/lark/oauth_agent_client_test.go`:

```go
package lark

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOAuthAgentClientResolvesUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/lark/oauth/user" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("x-internal-secret"); got != "shh" {
			t.Errorf("x-internal-secret = %q", got)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["code"] != "authcode" || body["redirect_uri"] != "http://localhost:3000/auth/callback" {
			t.Errorf("unexpected body: %v", body)
		}
		_, _ = w.Write([]byte(`{"user":{"union_id":"on_abc","open_id":"ou_x","name":"张三","avatar_url":"https://p.example/a.png"}}`))
	}))
	t.Cleanup(srv.Close)

	client := NewOAuthAgentClient(OAuthAgentClientConfig{BaseURL: srv.URL, InternalSecret: "shh"})
	user, err := client.ResolveOAuthUser(context.Background(), "authcode", "http://localhost:3000/auth/callback")
	if err != nil {
		t.Fatalf("ResolveOAuthUser: %v", err)
	}
	if user.UnionID != "on_abc" || user.Name != "张三" {
		t.Errorf("unexpected user: %+v", user)
	}
}

func TestOAuthAgentClientRejectsMissingUnionID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"user":{"name":"张三"}}`))
	}))
	t.Cleanup(srv.Close)

	client := NewOAuthAgentClient(OAuthAgentClientConfig{BaseURL: srv.URL, InternalSecret: "shh"})
	_, err := client.ResolveOAuthUser(context.Background(), "authcode", "http://localhost:3000/auth/callback")
	if err == nil || !strings.Contains(err.Error(), "union_id") {
		t.Fatalf("expected union_id error, got: %v", err)
	}
}

func TestOAuthAgentClientSurfacesUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"ok":false,"error":"failed to resolve Feishu OAuth user"}`))
	}))
	t.Cleanup(srv.Close)

	client := NewOAuthAgentClient(OAuthAgentClientConfig{BaseURL: srv.URL, InternalSecret: "shh"})
	_, err := client.ResolveOAuthUser(context.Background(), "authcode", "http://localhost:3000/auth/callback")
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("expected HTTP 502 error, got: %v", err)
	}
}

func TestOAuthAgentClientIsConfigured(t *testing.T) {
	if NewOAuthAgentClient(OAuthAgentClientConfig{BaseURL: "http://x"}).IsConfigured() {
		t.Error("missing secret should not be configured")
	}
	if !NewOAuthAgentClient(OAuthAgentClientConfig{BaseURL: "http://x", InternalSecret: "s"}).IsConfigured() {
		t.Error("base+secret should be configured")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/yuanzhan/Documents/multica-railway/multica/server && go test ./internal/integrations/lark/ -run 'OAuthAgent' -v`
Expected: FAIL (compile error: `undefined: NewOAuthAgentClient`)

- [ ] **Step 3: Write the implementation**

Create `server/internal/integrations/lark/oauth_agent_client.go`:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/yuanzhan/Documents/multica-railway/multica/server && gofmt -l ./internal/integrations/lark/ && go vet ./internal/integrations/lark/ && go test ./internal/integrations/lark/ -run 'OAuth' -v`
Expected: PASS (8 tests total), gofmt prints nothing.

- [ ] **Step 5: Commit**

```bash
cd /Users/yuanzhan/Documents/multica-railway/multica
git add server/internal/integrations/lark/oauth_agent_client.go server/internal/integrations/lark/oauth_agent_client_test.go
git commit -m "feat(lark): add private-agent OAuth client for login identity resolution"
```

---

### Task 3: Go — handler field, LarkLogin, /api/config, router wiring

**Files:**
- Modify: `server/internal/handler/handler.go` (struct field, ~line 205 after `DingTalkOAuth`)
- Modify: `server/internal/handler/auth.go` (request type near line 470; handler after `DingTalkLogin`, ~line 740)
- Modify: `server/internal/handler/config.go` (AppConfig field + GetConfig)
- Modify: `server/cmd/server/router.go` (wiring after DingTalk block ~line 216; route in the non-locked auth block ~line 672)
- Test: `server/internal/handler/auth_lark_test.go` (new)

`handler.go` and `router.go` already import `integrations/lark` (lines 26 / 29) — no import edits needed anywhere.

- [ ] **Step 1: Add the `LarkOAuth` field**

In `server/internal/handler/handler.go`, change:

```go
	DingTalk      dingtalk.CapabilityClient
	DingTalkOAuth dingtalk.OAuthClient
	cfg           Config
```

to:

```go
	DingTalk      dingtalk.CapabilityClient
	DingTalkOAuth dingtalk.OAuthClient
	// LarkOAuth resolves Feishu login codes for POST /auth/lark. Production
	// prefers the private channel agent (LARK_AGENT_BASE_URL) so the app
	// secret stays outside this backend; the direct client remains available
	// for self-hosted / local-dev deployments.
	LarkOAuth lark.OAuthClient
	cfg       Config
```

- [ ] **Step 2: Write the failing handler test**

Create `server/internal/handler/auth_lark_test.go`:

```go
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/lark"
)

type fakeLarkOAuth struct {
	user       lark.OAuthUser
	err        error
	configured bool
}

func (f fakeLarkOAuth) IsConfigured() bool { return f.configured }
func (f fakeLarkOAuth) ResolveOAuthUser(ctx context.Context, code, redirectURI string) (lark.OAuthUser, error) {
	return f.user, f.err
}

func postLarkLogin(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/auth/lark", bytes.NewBufferString(body))
	w := httptest.NewRecorder()
	testHandler.LarkLogin(w, req)
	return w
}

func TestLarkLoginSuccess(t *testing.T) {
	orig := testHandler.LarkOAuth
	testHandler.LarkOAuth = fakeLarkOAuth{
		configured: true,
		user: lark.OAuthUser{
			UnionID:   "on_handler_test_union",
			Name:      "飞书用户",
			AvatarURL: "https://p.example/avatar.png",
		},
	}
	defer func() { testHandler.LarkOAuth = orig }()

	w := postLarkLogin(t, `{"code":"authcode","redirect_uri":"http://localhost:3000/auth/callback"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp LoginResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Token == "" {
		t.Error("expected a JWT token")
	}
	if resp.User.Email != "on_handler_test_union@feishu.cn" {
		t.Errorf("email = %q, want synthetic on_handler_test_union@feishu.cn", resp.User.Email)
	}
}

func TestLarkLoginNotConfigured(t *testing.T) {
	orig := testHandler.LarkOAuth
	testHandler.LarkOAuth = nil
	defer func() { testHandler.LarkOAuth = orig }()

	w := postLarkLogin(t, `{"code":"authcode"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestLarkLoginUpstreamFailure(t *testing.T) {
	orig := testHandler.LarkOAuth
	testHandler.LarkOAuth = fakeLarkOAuth{configured: true, err: context.DeadlineExceeded}
	defer func() { testHandler.LarkOAuth = orig }()

	w := postLarkLogin(t, `{"code":"authcode"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
}

func TestLarkLoginMissingUnionID(t *testing.T) {
	orig := testHandler.LarkOAuth
	testHandler.LarkOAuth = fakeLarkOAuth{configured: true, user: lark.OAuthUser{Name: "张三"}}
	defer func() { testHandler.LarkOAuth = orig }()

	w := postLarkLogin(t, `{"code":"authcode"}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", w.Code)
	}
}

func TestLarkLoginMissingCode(t *testing.T) {
	orig := testHandler.LarkOAuth
	testHandler.LarkOAuth = fakeLarkOAuth{configured: true}
	defer func() { testHandler.LarkOAuth = orig }()

	w := postLarkLogin(t, `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd /Users/yuanzhan/Documents/multica-railway/multica/server && go test ./internal/handler/ -run 'TestLarkLogin' -v`
Expected: FAIL (compile error: `testHandler.LarkLogin undefined`). Note: handler tests need the local Postgres (`make dev` DB); `TestMain` exits 0 with a "Skipping tests" message when it is unreachable — if you see that, start the DB first, then treat compile success + skip as NOT sufficient.

- [ ] **Step 4: Implement `LarkLogin`**

In `server/internal/handler/auth.go`, directly under the `DingTalkLoginRequest` type (line ~470), add:

```go
// LarkLoginRequest carries the one-time authorization code from Feishu's
// OAuth redirect plus the redirect_uri used on the authorize step — Feishu
// re-validates redirect_uri during the token exchange (DingTalk does not,
// Google does).
type LarkLoginRequest struct {
	Code        string `json:"code"`
	RedirectURI string `json:"redirect_uri"`
}
```

Directly after the closing brace of `DingTalkLogin` (before `IssueCliToken`), add:

```go
// LarkLogin mirrors DingTalkLogin for Feishu (飞书 / Lark) OAuth. The frontend
// sends the browser to accounts.feishu.cn/open-apis/authen/v1/authorize and
// receives a `code` on the shared /auth/callback page (provider disambiguated
// by a "provider:lark" marker in state); this handler exchanges it for a user
// access token, reads the Feishu profile, and maps that identity onto a
// Multica user.
//
// Like DingTalk, Feishu profiles are not guaranteed to expose a usable email,
// so identity is keyed on a synthetic address derived from the stable
// union_id (`<union_id>@feishu.cn`) — never the mutable display name, and
// never the real email even when present, so the identity key can't drift.
// The real name/avatar are backfilled for display only. For an 企业自建应用,
// only members of that Feishu org can complete the flow.
func (h *Handler) LarkLogin(w http.ResponseWriter, r *http.Request) {
	var req LarkLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Code == "" {
		writeError(w, http.StatusBadRequest, "code is required")
		return
	}

	if h.LarkOAuth == nil || !h.LarkOAuth.IsConfigured() {
		writeError(w, http.StatusServiceUnavailable, "Feishu login is not configured")
		return
	}

	lkUser, err := h.LarkOAuth.ResolveOAuthUser(r.Context(), req.Code, req.RedirectURI)
	if err != nil {
		slog.Error("lark oauth user resolution failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusBadGateway, "failed to resolve Feishu user")
		return
	}

	if lkUser.UnionID == "" {
		writeError(w, http.StatusBadGateway, "Feishu account has no union_id")
		return
	}

	// Synthesize a stable, unique identity address from the union_id — the
	// same scheme as DingTalk (`<unionId>@dingtalk.com`). The union_id is
	// kept verbatim so two distinct ids can never collide and it round-trips
	// deterministically through GetUserByEmail on every subsequent login.
	email := lkUser.UnionID + "@feishu.cn"

	user, isNew, err := h.findOrCreateUser(r.Context(), email)
	if err != nil {
		var signupErr SignupError
		if errors.As(err, &signupErr) {
			writeError(w, http.StatusForbidden, signupErr.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}
	if isNew {
		evt := analytics.Signup(uuidToString(user.ID), user.Email, signupSourceFromRequest(r))
		evt.Properties["auth_method"] = "lark"
		obsmetrics.RecordEvent(h.Analytics, h.Metrics, evt)
	}

	// Backfill display name/avatar from the Feishu profile on first login
	// (default name is the email prefix = the union_id) or when still unset.
	needsUpdate := false
	newName := user.Name
	newAvatar := user.AvatarUrl

	if lkUser.Name != "" && user.Name == strings.Split(email, "@")[0] {
		newName = lkUser.Name
		needsUpdate = true
	}
	if lkUser.AvatarURL != "" && !user.AvatarUrl.Valid {
		newAvatar = pgtype.Text{String: lkUser.AvatarURL, Valid: true}
		needsUpdate = true
	}

	if needsUpdate {
		updated, err := h.Queries.UpdateUser(r.Context(), db.UpdateUserParams{
			ID:        user.ID,
			Name:      newName,
			AvatarUrl: newAvatar,
		})
		if err == nil {
			user = updated
		}
	}

	tokenString, err := h.issueJWT(user)
	if err != nil {
		slog.Warn("lark login failed", append(logger.RequestAttrs(r), "error", err, "email", email)...)
		writeError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	if err := auth.SetAuthCookies(w, tokenString); err != nil {
		slog.Warn("failed to set auth cookies", "error", err)
	}

	if h.CFSigner != nil {
		for _, cookie := range h.CFSigner.SignedCookies(time.Now().Add(72 * time.Hour)) {
			http.SetCookie(w, cookie)
		}
	}

	slog.Info("user logged in via lark", append(logger.RequestAttrs(r), "user_id", uuidToString(user.ID), "email", user.Email)...)
	writeJSON(w, http.StatusOK, LoginResponse{
		Token: tokenString,
		User:  userToResponse(user),
	})
}
```

- [ ] **Step 5: Expose `lark_client_id` in /api/config**

In `server/internal/handler/config.go`, after the `DingtalkOnly` field, add:

```go
	// LarkClientID is the Feishu app's AppID (cli_xxx). When set, the web app
	// renders the "使用飞书登录" button and builds the
	// accounts.feishu.cn/open-apis/authen/v1/authorize URL with it. Non-secret
	// — required on this backend even when the code exchange runs through the
	// private agent, because the login page needs it for the authorize URL
	// (same split as DINGTALK_CLIENT_ID). Omitted when empty.
	LarkClientID string `json:"lark_client_id,omitempty"`
```

In `GetConfig`, after `DingtalkOnly: DingtalkOnlyEnabled(),` add:

```go
		LarkClientID:              os.Getenv("LARK_CLIENT_ID"),
```

- [ ] **Step 6: Wire the client tiering and route in router.go**

In `server/cmd/server/router.go`, immediately after the closing `}` of the DingTalk wiring chain (the `else { slog.Info("dingtalk integration disabled ...") }` at ~line 216), add:

```go
	// Lark (Feishu) login identity resolution — same tiering as DingTalk:
	// prefer the private channel agent so the app secret stays outside this
	// backend, fall back to the direct open-API client for self-host/dev.
	if larkAgentBase := strings.TrimSpace(os.Getenv("LARK_AGENT_BASE_URL")); larkAgentBase != "" {
		agentClient := lark.NewOAuthAgentClient(lark.OAuthAgentClientConfig{
			BaseURL:        larkAgentBase,
			InternalSecret: strings.TrimSpace(os.Getenv("LARK_AGENT_INTERNAL_SECRET")),
			Logger:         slog.Default(),
		})
		h.LarkOAuth = agentClient
		if agentClient.IsConfigured() {
			slog.Info("lark oauth enabled via private agent", "base_url", larkAgentBase)
		} else {
			slog.Info("lark oauth disabled (LARK_AGENT_INTERNAL_SECRET not set)")
		}
	} else if larkClientID, larkClientSecret := strings.TrimSpace(os.Getenv("LARK_CLIENT_ID")), strings.TrimSpace(os.Getenv("LARK_CLIENT_SECRET")); larkClientID != "" && larkClientSecret != "" {
		h.LarkOAuth = lark.NewOAuthHTTPClient(lark.OAuthConfig{
			ClientID:     larkClientID,
			ClientSecret: larkClientSecret,
			APIBase:      strings.TrimSpace(os.Getenv("LARK_OPENAPI_BASE")),
			Logger:       slog.Default(),
		})
		slog.Info("lark oauth enabled via direct client")
	} else {
		slog.Info("lark oauth disabled (LARK_CLIENT_ID or LARK_CLIENT_SECRET not set)")
	}
```

Then register the route inside the existing `if !handler.DingtalkOnlyEnabled() { ... }` block (LOGIN_DINGTALK_ONLY means DingTalk is the SOLE way in, so the lark route must be closed under the lock too):

```go
	if !handler.DingtalkOnlyEnabled() {
		r.With(authRL).Post("/auth/send-code", h.SendCode)
		r.With(authVerifyRL).Post("/auth/verify-code", h.VerifyCode)
		r.With(authRL).Post("/auth/google", h.GoogleLogin)
		r.With(authRL).Post("/auth/lark", h.LarkLogin)
	}
```

- [ ] **Step 7: Run the tests and build**

Run: `cd /Users/yuanzhan/Documents/multica-railway/multica/server && gofmt -l ./internal/ ./cmd/ && go vet ./... && go build ./... && go test ./internal/handler/ -run 'TestLarkLogin' -v`
Expected: build clean, gofmt prints nothing, 5 tests PASS (or the whole package skips with "Skipping tests: database not reachable" — in that case start the dev DB and re-run).

- [ ] **Step 8: Commit**

```bash
cd /Users/yuanzhan/Documents/multica-railway/multica
git add server/internal/handler/handler.go server/internal/handler/auth.go server/internal/handler/auth_lark_test.go server/internal/handler/config.go server/cmd/server/router.go
git commit -m "feat(auth): add Feishu (飞书) OAuth login endpoint /auth/lark"
```

---

### Task 4: packages/core — config schema, API client, auth store, config store

**Files:**
- Modify: `packages/core/api/schemas.ts` (interface ~line 41, schema ~line 187, EMPTY ~line 200)
- Modify: `packages/core/api/client.ts` (~line 445, after `dingtalkLogin`)
- Modify: `packages/core/auth/store.ts` (interface ~line 24, impl ~line 118)
- Modify: `packages/core/config/index.ts` (state, defaults, `setAuthConfig`)
- Modify: `packages/core/platform/auth-initializer.tsx` (~line 62)
- Test: `packages/core/api/schemas.test.ts` (after the dingtalk drift describe, ~line 435)

- [ ] **Step 1: Write the failing schema drift test**

In `packages/core/api/schemas.test.ts`, after the `AppConfigSchema dingtalk_client_id drift` describe block, add:

```ts
describe("AppConfigSchema lark_client_id drift", () => {
  it("parses lark_client_id when the server provides it", () => {
    const parsed = AppConfigSchema.parse({ lark_client_id: "cli_feishuappid" });
    expect(parsed.lark_client_id).toBe("cli_feishuappid");
  });

  it("leaves lark_client_id undefined when a Feishu-less server omits it", () => {
    const parsed = AppConfigSchema.parse({ cdn_domain: "cdn.example.com" });
    expect(parsed.lark_client_id).toBeUndefined();
  });

  it("drops a malformed lark_client_id instead of failing the parse", () => {
    const parsed = AppConfigSchema.parse({ lark_client_id: 12345 });
    expect(parsed.lark_client_id).toBeUndefined();
  });
});
```

Note: check how the existing dingtalk drift tests treat malformed values — `OptionalStringSchema` in this codebase is lenient (preprocess to undefined on non-string). If the third test fails because `OptionalStringSchema` throws on numbers instead, mirror whatever the `dingtalk_client_id` tests assert and drop the third case.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/yuanzhan/Documents/multica-railway/multica && pnpm vitest run packages/core/api/schemas.test.ts` (if vitest is not runnable at the root, run `pnpm test` and filter output)
Expected: FAIL — `parsed.lark_client_id` is undefined in the first test because the schema strips unknown handling to passthrough; the typed accessor does not exist yet (TS error on `parsed.lark_client_id`).

- [ ] **Step 3: Implement the core changes**

`packages/core/api/schemas.ts` — three edits:

In `AppConfigResponse` after `dingtalk_only?: boolean;`:

```ts
  // Feishu (Lark) app AppID. When present the login screen renders the
  // "Continue with Feishu" button. Older servers omit it.
  lark_client_id?: string;
```

In `AppConfigSchema` after `dingtalk_only: ...`:

```ts
  lark_client_id: OptionalStringSchema,
```

In `EMPTY_APP_CONFIG` after `dingtalk_only: false,`:

```ts
  lark_client_id: "",
```

`packages/core/api/client.ts` — after `dingtalkLogin`:

```ts
  // Feishu (Lark) returns the grant as `code` and, unlike DingTalk, the
  // backend must send the same redirect_uri again on the token exchange.
  async larkLogin(code: string, redirectUri: string): Promise<LoginResponse> {
    return this.fetch("/auth/lark", {
      method: "POST",
      body: JSON.stringify({ code, redirect_uri: redirectUri }),
    });
  }
```

`packages/core/auth/store.ts` — in `AuthState` after `loginWithDingtalk`:

```ts
  loginWithLark: (code: string, redirectUri: string) => Promise<User>;
```

In the store implementation after the `loginWithDingtalk` function:

```ts
    loginWithLark: async (code: string, redirectUri: string) => {
      const { token, user } = await api.larkLogin(code, redirectUri);
      if (!cookieAuth) {
        storage.setItem("multica_token", token);
        api.setToken(token);
      }
      onLogin?.();
      identifyAnalytics(user.id, { email: user.email, name: user.name });
      set({ user });
      return user;
    },
```

`packages/core/config/index.ts` — four edits mirroring `dingtalkClientId`:

1. In `ConfigState` after `dingtalkOnly: boolean;` (keep the comment style):

```ts
  larkClientId: string;
```

2. In the `setAuthConfig` parameter type after `dingtalkOnly?: boolean;`:

```ts
    larkClientId?: string;
```

3. In the store defaults after `dingtalkOnly: false,`:

```ts
  larkClientId: "",
```

4. In the `setAuthConfig` implementation, add `larkClientId = ""` to the destructured params and `larkClientId,` to the `set({...})` object.

`packages/core/platform/auth-initializer.tsx` — in the `setAuthConfig` call after `dingtalkOnly: cfg.dingtalk_only === true,`:

```ts
          larkClientId: cfg.lark_client_id,
```

- [ ] **Step 4: Run tests and typecheck**

Run: `cd /Users/yuanzhan/Documents/multica-railway/multica && pnpm vitest run packages/core/api/schemas.test.ts && pnpm typecheck`
Expected: schema tests PASS; typecheck clean.

- [ ] **Step 5: Commit**

```bash
cd /Users/yuanzhan/Documents/multica-railway/multica
git add packages/core/api/schemas.ts packages/core/api/schemas.test.ts packages/core/api/client.ts packages/core/auth/store.ts packages/core/config/index.ts packages/core/platform/auth-initializer.tsx
git commit -m "feat(core): add Feishu login API client, auth store action, and config plumbing"
```

---

### Task 5: packages/views — login page button + i18n

**Files:**
- Modify: `packages/views/auth/login-page.tsx`
- Modify: `packages/views/locales/en/auth.json`, `packages/views/locales/zh-Hans/auth.json`, `packages/views/locales/ja/auth.json`, `packages/views/locales/ko/auth.json` (all four — `locales/parity.test.ts` enforces key parity)
- Test: `packages/views/auth/login-page.test.tsx`

- [ ] **Step 1: Write the failing component test**

In `packages/views/auth/login-page.test.tsx`, add at the end of the file (top-level, alongside the other describes):

```tsx
describe("Feishu (Lark) login", () => {
  const larkConfig = {
    clientId: "cli_testapp",
    redirectUri: "http://localhost:3000/auth/callback",
  };

  it("renders the Feishu button when lark config is provided", () => {
    renderWithI18n(<LoginPage onSuccess={vi.fn()} lark={larkConfig} />);
    expect(
      screen.getByRole("button", { name: /continue with feishu/i }),
    ).toBeInTheDocument();
  });

  it("hides the Feishu button in DingTalk-only mode", () => {
    renderWithI18n(
      <LoginPage
        onSuccess={vi.fn()}
        dingtalk={{
          clientId: "ding_test",
          redirectUri: "http://localhost:3000/auth/callback",
        }}
        dingtalkOnly
        lark={larkConfig}
      />,
    );
    expect(
      screen.queryByRole("button", { name: /continue with feishu/i }),
    ).not.toBeInTheDocument();
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/yuanzhan/Documents/multica-railway/multica && pnpm vitest run packages/views/auth/login-page.test.tsx`
Expected: FAIL — first test cannot find the button (and TS complains about the unknown `lark` prop).

- [ ] **Step 3: Add the i18n keys (all four locales)**

`packages/views/locales/en/auth.json` — inside `"signin"`, after the `"dingtalk"` line:

```json
    "lark": "Continue with Feishu",
```

`packages/views/locales/zh-Hans/auth.json` — same position:

```json
    "lark": "使用飞书登录",
```

`packages/views/locales/ja/auth.json` — same position:

```json
    "lark": "Feishuで続ける",
```

`packages/views/locales/ko/auth.json` — same position:

```json
    "lark": "Feishu로 계속",
```

- [ ] **Step 4: Implement the login page changes**

In `packages/views/auth/login-page.tsx`:

1. After the `DingtalkAuthConfig` interface, add:

```tsx
interface LarkAuthConfig {
  /** Feishu app AppID (cli_xxx). */
  clientId: string;
  redirectUri: string;
  /** Opaque state passed through Feishu OAuth. Carries a "provider:lark"
   *  marker plus platform/next/CLI params so the shared /auth/callback page
   *  can tell which provider to exchange the code with. */
  state?: string;
}
```

2. In `LoginPageProps`, after the `dingtalkOnly?: boolean;` entry add:

```tsx
  /** Feishu (Lark) OAuth config. Omit to disable Feishu login. */
  lark?: LarkAuthConfig;
```

and after `onDingtalkLogin?: () => void;` add:

```tsx
  /** Override Feishu login handler (e.g. desktop opens browser externally). When provided, renders the Feishu button even if `lark` config is omitted. */
  onLarkLogin?: () => void;
```

3. Add `lark,` and `onLarkLogin,` to the destructured props of `LoginPage` (next to their dingtalk counterparts).

4. After `handleDingtalkLogin`, add:

```tsx
  const handleLarkLogin = () => {
    if (onLarkLogin) {
      onLarkLogin();
      return;
    }
    if (!lark) return;
    // Feishu OAuth: the browser goes to the Feishu authorize page, which
    // returns a `code` to redirectUri. Feishu re-validates redirect_uri on
    // the server-side token exchange, so the callback page must send the
    // exact same value to /auth/lark.
    const params = new URLSearchParams({
      client_id: lark.clientId,
      redirect_uri: lark.redirectUri,
      response_type: "code",
    });
    if (lark.state) params.set("state", lark.state);
    window.location.href = `https://accounts.feishu.cn/open-apis/authen/v1/authorize?${params}`;
  };
```

5. After `const hasDingtalk = ...` / `dingtalkOnlyMode` / `showGoogle` lines, add:

```tsx
  const hasLark = Boolean(lark || onLarkLogin);
  // The DingTalk-only lock hides every other provider, Feishu included.
  const showLark = !dingtalkOnlyMode && hasLark;
```

6. Change the OAuth block gate from `{(showGoogle || hasDingtalk) && (` to `{(showGoogle || hasDingtalk || showLark) && (`.

7. After the closing `)}` of the `{hasDingtalk && (...)}` button, add:

```tsx
              {showLark && (
                <Button
                  type="button"
                  variant="outline"
                  className="w-full"
                  size="lg"
                  onClick={handleLarkLogin}
                  disabled={loading}
                >
                  <svg
                    className="mr-2 h-4 w-4"
                    viewBox="0 0 24 24"
                    fill="none"
                    stroke="#3370FF"
                    strokeWidth="2"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    aria-hidden="true"
                  >
                    <path d="M22 2 11 13" />
                    <path d="m22 2-7 20-4-9-9-4Z" />
                  </svg>
                  {t(($) => $.signin.lark)}
                </Button>
              )}
```

- [ ] **Step 5: Run the tests**

Run: `cd /Users/yuanzhan/Documents/multica-railway/multica && pnpm vitest run packages/views/auth/login-page.test.tsx packages/views/locales/parity.test.ts`
Expected: PASS (new tests + locale parity).

- [ ] **Step 6: Commit**

```bash
cd /Users/yuanzhan/Documents/multica-railway/multica
git add packages/views/auth/login-page.tsx packages/views/auth/login-page.test.tsx packages/views/locales/en/auth.json packages/views/locales/zh-Hans/auth.json packages/views/locales/ja/auth.json packages/views/locales/ko/auth.json
git commit -m "feat(views): add Feishu login button to the shared login page"
```

---

### Task 6: apps — web login/callback wiring + desktop wiring

**Files:**
- Modify: `apps/web/app/(auth)/login/page.tsx` (config selector ~line 62, state ~line 150, LoginPage props ~line 227)
- Modify: `apps/web/app/auth/callback/page.tsx` (provider branch in the effect)
- Modify: `apps/desktop/src/renderer/src/pages/login.tsx`

- [ ] **Step 1: Wire the web login page**

In `apps/web/app/(auth)/login/page.tsx`:

1. After `const dingtalkOnly = useConfigStore((state) => state.dingtalkOnly);` add:

```tsx
  const larkClientId = useConfigStore((state) => state.larkClientId);
```

2. After the `dingtalkState` declaration add:

```tsx
  // Feishu shares /auth/callback with Google and DingTalk; its state carries
  // a "provider:lark" marker so the callback exchanges the code with the
  // right provider (Feishu and Google both return the grant as `code`).
  const larkState = googleState
    ? `provider:lark,${googleState}`
    : "provider:lark";
```

3. In the `<LoginPage ... />` element, after the `dingtalk={...}` prop add:

```tsx
      lark={
        larkClientId
          ? {
              clientId: larkClientId,
              redirectUri: `${window.location.origin}/auth/callback`,
              state: larkState,
            }
          : undefined
      }
```

- [ ] **Step 2: Wire the callback page**

In `apps/web/app/auth/callback/page.tsx`:

1. After `const loginWithDingtalk = useAuthStore((s) => s.loginWithDingtalk);` add:

```tsx
  const loginWithLark = useAuthStore((s) => s.loginWithLark);
```

2. After `const isDingtalk = stateParts.includes("provider:dingtalk");` add:

```tsx
    const isLark = stateParts.includes("provider:lark");
```

3. Replace the three provider-dispatch expressions. CLI flow:

```tsx
      (isDingtalk
        ? api.dingtalkLogin(code)
        : isLark
          ? api.larkLogin(code, redirectUri)
          : api.googleLogin(code, redirectUri))
```

Desktop flow — same replacement for the second occurrence:

```tsx
      (isDingtalk
        ? api.dingtalkLogin(code)
        : isLark
          ? api.larkLogin(code, redirectUri)
          : api.googleLogin(code, redirectUri))
```

Web flow:

```tsx
      (isDingtalk
        ? loginWithDingtalk(code)
        : isLark
          ? loginWithLark(code, redirectUri)
          : loginWithGoogle(code, redirectUri))
```

4. Extend the effect dependency array from `[searchParams, loginWithGoogle, loginWithDingtalk, router, qc]` to `[searchParams, loginWithGoogle, loginWithDingtalk, loginWithLark, router, qc]`.

5. Update the comment above the `code` extraction to mention Feishu:

```tsx
    // Google and Feishu return the grant as `code`; DingTalk uses `authCode`.
```

- [ ] **Step 3: Wire the desktop login page**

In `apps/desktop/src/renderer/src/pages/login.tsx`:

1. After `const dingtalkOnly = useConfigStore((s) => s.dingtalkOnly);` add:

```tsx
  // Only offer the Feishu button when the backend actually has a Feishu app
  // configured — unlike Google/DingTalk this is gated on config so
  // Feishu-less deployments don't show a dead button.
  const larkClientId = useConfigStore((s) => s.larkClientId);
```

2. In the `<LoginPage ... />` element, after `onDingtalkLogin={openWebLogin}` add:

```tsx
          onLarkLogin={larkClientId && !dingtalkOnly ? openWebLogin : undefined}
```

- [ ] **Step 4: Typecheck and run the web tests**

Run: `cd /Users/yuanzhan/Documents/multica-railway/multica && pnpm typecheck && pnpm test`
Expected: clean typecheck; full TS test suite green (this also re-runs core/views tests from Tasks 4–5).

- [ ] **Step 5: Commit**

```bash
cd /Users/yuanzhan/Documents/multica-railway/multica
git add "apps/web/app/(auth)/login/page.tsx" apps/web/app/auth/callback/page.tsx apps/desktop/src/renderer/src/pages/login.tsx
git commit -m "feat(auth): wire Feishu login through web login/callback and desktop"
```

---

### Task 7: dingtalk-native-agent — Lark adapter + internal route (separate repo)

**Files (all under `/Users/yuanzhan/d1/dingtalk-native-agent`):**
- Create: `src/adapters/lark.ts`
- Modify: `src/config.ts`
- Modify: `src/container.ts`
- Modify: `src/routes/internal.ts`
- Modify: `src/routes/health.ts`

This repo has no test infra (typecheck only). Verification is `npm run typecheck` + a curl smoke in Task 8.

- [ ] **Step 1: Extend config**

In `src/config.ts`:

1. In the `AppConfig` interface, after the `dingtalk: {...};` block add:

```ts
  lark: {
    appId: string;
    appSecret: string;
    openapiBase: string;
  };
```

2. In `loadConfig()`'s returned object, after the `dingtalk: {...},` block add:

```ts
    lark: {
      appId: str('LARK_APP_ID'),
      appSecret: str('LARK_APP_SECRET'),
      openapiBase: str('LARK_OPENAPI_BASE', 'https://open.feishu.cn'),
    },
```

3. In `configSummary()`, after the `dingtalk: {...},` entry add:

```ts
    lark: {
      oauthConfigured: Boolean(cfg.lark.appId && cfg.lark.appSecret),
    },
```

- [ ] **Step 2: Create the Lark adapter**

Create `src/adapters/lark.ts`:

```ts
import type { AppConfig } from '../config';
import type { Logger } from '../utils/logger';
import { fetchJson, HttpError } from '../utils/http';

export interface LarkOAuthUser {
  union_id: string;
  open_id?: string;
  name?: string;
  avatar_url?: string;
  email?: string;
}

// Feishu's v2 token endpoint speaks RFC 6749 on failure:
// {"error":"...","error_description":"...","code":20063} — NOT the usual
// Feishu {code,msg} envelope. Success carries access_token.
interface OAuthTokenResponse {
  access_token?: string;
  error?: string;
  error_description?: string;
  code?: number;
}

interface UserInfoResponse {
  code?: number;
  msg?: string;
  data?: {
    union_id?: string;
    open_id?: string;
    name?: string;
    en_name?: string;
    avatar_url?: string;
    email?: string;
    enterprise_email?: string;
  };
}

export class LarkClient {
  constructor(
    private readonly cfg: AppConfig['lark'],
    private readonly logger: Logger,
  ) {}

  isConfigured(): boolean {
    return Boolean(this.cfg.appId && this.cfg.appSecret);
  }

  /**
   * Exchange a Feishu login code for the user identity. redirectUri must be
   * the exact redirect_uri used on the authorize redirect — Feishu
   * re-validates it here (DingTalk does not).
   */
  async resolveOAuthUser(code: string, redirectUri: string): Promise<LarkOAuthUser> {
    const trimmed = code.trim();
    if (!trimmed) throw new Error('auth code is required');
    const base = this.cfg.openapiBase.replace(/\/$/, '');

    let token: OAuthTokenResponse;
    try {
      token = await fetchJson<OAuthTokenResponse>(`${base}/open-apis/authen/v2/oauth/token`, {
        method: 'POST',
        body: {
          grant_type: 'authorization_code',
          client_id: this.cfg.appId,
          client_secret: this.cfg.appSecret,
          code: trimmed,
          redirect_uri: redirectUri,
        },
        timeoutMs: 8000,
      });
    } catch (err) {
      // fetchJson throws HttpError on non-2xx; the RFC 6749 error body is in
      // bodyText — surface error_description for actionable logs.
      if (err instanceof HttpError) {
        let detail = err.bodyText;
        try {
          const parsed = JSON.parse(err.bodyText) as OAuthTokenResponse;
          detail = parsed.error_description ?? parsed.error ?? err.bodyText;
        } catch {
          // keep raw body
        }
        throw new Error(`Feishu token exchange failed (HTTP ${err.status}): ${detail}`);
      }
      throw err;
    }
    if (!token.access_token) {
      throw new Error(
        `Feishu token exchange failed: ${token.error_description ?? token.error ?? 'no access_token'}`,
      );
    }

    const info = await fetchJson<UserInfoResponse>(`${base}/open-apis/authen/v1/user_info`, {
      method: 'GET',
      headers: { authorization: `Bearer ${token.access_token}` },
      timeoutMs: 8000,
    });
    if (info.code !== 0 || !info.data) {
      throw new Error(`Feishu user_info failed: code=${info.code} msg=${info.msg ?? ''}`);
    }
    const unionId = info.data.union_id?.trim();
    if (!unionId) throw new Error('Feishu account has no union_id');
    return {
      union_id: unionId,
      open_id: info.data.open_id,
      name: info.data.name?.trim() || info.data.en_name?.trim() || undefined,
      avatar_url: info.data.avatar_url,
      email: info.data.email?.trim() || info.data.enterprise_email?.trim() || undefined,
    };
  }
}
```

- [ ] **Step 3: Register in the container**

In `src/container.ts`:

1. Add the import next to the DingTalk adapter import:

```ts
import { LarkClient } from './adapters/lark';
```

2. In the `Services` interface, after `dingtalk: DingTalkClient;` add:

```ts
  lark: LarkClient;
```

3. In `buildServices`, after the `const dingtalk = new DingTalkClient(...)` line add:

```ts
  const lark = new LarkClient(cfg.lark, logger.child({ mod: 'lark' }));
```

4. Add `lark,` to the `const services: Services = { ... }` object literal.

- [ ] **Step 4: Add the internal route**

In `src/routes/internal.ts`, after the `/internal/dingtalk/oauth/user` route handler, add:

```ts
  app.post('/internal/lark/oauth/user', async (req: FastifyRequest, reply) => {
    if (!requireInternalApi(req, reply, services)) return reply;
    if (!services.lark.isConfigured()) {
      reply.code(503);
      return { ok: false, error: 'Lark OAuth is not configured' };
    }

    const body = (req.body ?? {}) as { code?: string; redirect_uri?: string };
    const code = (body.code ?? '').trim();
    const redirectUri = (body.redirect_uri ?? '').trim();
    if (!code) {
      reply.code(400);
      return { ok: false, error: 'code is required' };
    }
    if (code.length > 4096) {
      reply.code(400);
      return { ok: false, error: 'code is too long' };
    }
    if (!redirectUri) {
      reply.code(400);
      return { ok: false, error: 'redirect_uri is required' };
    }

    try {
      const user = await services.lark.resolveOAuthUser(code, redirectUri);
      return { user };
    } catch (err) {
      services.logger.warn('internal.lark.oauth_user_failed', {
        reason: err instanceof Error ? err.message : String(err),
      });
      reply.code(502);
      return { ok: false, error: 'failed to resolve Feishu OAuth user' };
    }
  });
```

- [ ] **Step 5: Advertise on /healthz**

In `src/routes/health.ts`, add to the endpoints array after `'POST /internal/dingtalk/oauth/user',`:

```ts
      'POST /internal/lark/oauth/user',
```

- [ ] **Step 6: Typecheck and commit (this repo's git)**

Run: `cd /Users/yuanzhan/d1/dingtalk-native-agent && npm run typecheck`
Expected: clean.

```bash
cd /Users/yuanzhan/d1/dingtalk-native-agent
git add src/adapters/lark.ts src/config.ts src/container.ts src/routes/internal.ts src/routes/health.ts
git commit -m "feat(lark): add /internal/lark/oauth/user for Multica Feishu login"
```

---

### Task 8: End-to-end smoke verification

No new files. Proves the full chain reaches the real Feishu API. A real `code` requires the browser flow (needs the redirect URL whitelisted in the Feishu console — operator action), so the smoke asserts the deterministic failure shape with a fake code, exactly like the DingTalk integration was verified.

- [ ] **Step 1: Retrieve the app secret from the lark-cli keychain (do NOT echo it)**

```bash
LARK_SECRET=$(security find-generic-password -a "appsecret:cli_aab264b001f8dbcf" -w 2>/dev/null || security find-generic-password -s "appsecret:cli_aab264b001f8dbcf" -w)
[ -n "$LARK_SECRET" ] && echo "secret retrieved (len ${#LARK_SECRET})"
```

If both fail, list candidates with `security dump-keychain -d login.keychain 2>/dev/null | grep -i lark` — or ask the user for the secret. Never print or commit it.

- [ ] **Step 2: Run the agent locally and smoke the internal route**

```bash
cd /Users/yuanzhan/d1/dingtalk-native-agent
LARK_APP_ID=cli_aab264b001f8dbcf LARK_APP_SECRET="$LARK_SECRET" INTERNAL_API_SECRET=dev-secret PORT=3100 npm run dev
```

In another shell:

```bash
curl -s http://localhost:3100/healthz | grep lark
curl -s -X POST http://localhost:3100/internal/lark/oauth/user \
  -H 'x-internal-secret: dev-secret' -H 'content-type: application/json' \
  -d '{"code":"fake","redirect_uri":"http://localhost:3000/auth/callback"}'
```

Expected: healthz lists the lark route with `oauthConfigured: true`; the POST returns HTTP 502 `{"ok":false,"error":"failed to resolve Feishu OAuth user"}` and the agent log shows `Feishu token exchange failed (HTTP 400): ...code is invalid...` — proving credentials + endpoint + parsing all work up to Feishu rejecting the fake code. A 401 means the internal secret didn't match; a 503 means env didn't load.

- [ ] **Step 3: Smoke the Go server in agent mode**

With the agent still running, start the backend with:

```bash
cd /Users/yuanzhan/Documents/multica-railway/multica
LARK_AGENT_BASE_URL=http://localhost:3100 LARK_AGENT_INTERNAL_SECRET=dev-secret LARK_CLIENT_ID=cli_aab264b001f8dbcf make server
```

Then:

```bash
curl -s http://localhost:8080/api/config | grep -o '"lark_client_id":"[^"]*"'
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://localhost:8080/auth/lark \
  -H 'content-type: application/json' \
  -d '{"code":"fake","redirect_uri":"http://localhost:3000/auth/callback"}'
```

Expected: config shows `"lark_client_id":"cli_aab264b001f8dbcf"`; `/auth/lark` returns `502`. Server log shows `lark oauth enabled via private agent`. (If the server port differs, check `make server` output for the actual port.)

- [ ] **Step 4: Stop the smoke processes, record results, final checks**

Kill the dev agent and server. Then run the broad checks:

```bash
cd /Users/yuanzhan/Documents/multica-railway/multica && pnpm typecheck && pnpm test
cd server && go build ./... && go test ./internal/integrations/lark/ ./internal/handler/ -run 'Lark|OAuth'
```

Append a `Result:` section to `docs/superpowers/specs/2026-07-02-feishu-login-design.md` recording what was verified and the remaining operator steps (Feishu console redirect URL whitelist; Railway env: agent `LARK_APP_ID/SECRET`, backend `LARK_AGENT_BASE_URL`, `LARK_AGENT_INTERNAL_SECRET`, `LARK_CLIENT_ID`), then commit:

```bash
cd /Users/yuanzhan/Documents/multica-railway/multica
git add docs/superpowers/specs/2026-07-02-feishu-login-design.md
git commit -m "docs(lark): record Feishu login verification results and operator steps"
```

---

## Out of scope (do not build)

- Feishu org member search / group invite (DingTalk parity — separate round).
- `LOGIN_LARK_ONLY` lock.
- Lark international brand (`accounts.larksuite.com` authorize URL) — the API base is env-overridable but the authorize URL in `login-page.tsx` is feishu-brand only, matching the hardcoded `login.dingtalk.com`.
- lark-cli as a runtime dependency — it is a dev-verification tool only.
