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
