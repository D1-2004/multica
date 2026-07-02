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
