package dws

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

func meGateway(t *testing.T) (*fakeGateway, string) {
	return startGateway(t, func(tool string, _ map[string]any, token string) (int, string) {
		if tool == "get_current_user_profile" && token == "uat-1" {
			return ok(meResult)
		}
		return 400, `{"error":"Missing service_id or access_key"}`
	})
}

func TestNewExchangesTheCodeAndVerifiesTheIdentity(t *testing.T) {
	gw, gwURL := meGateway(t)
	oauth, authURL := startOAuth(t, func(path string, body map[string]string) (int, string) {
		return 200, `{"accessToken":"uat-1","refreshToken":"rt-1","expiresIn":7200,"corpId":"ding1"}`
	})
	c, err := New(context.Background(), Config{GatewayURL: gwURL, AuthURL: authURL}, AuthCode{Code: "code-1", ClientID: "client-1"})
	if err != nil {
		t.Fatal(err)
	}
	if oauth.paths[0] != "/oauth2/getToken" {
		t.Fatalf("hosted exchange hit %s", oauth.paths[0])
	}
	want := map[string]string{"clientId": "client-1", "authCode": "code-1", "grantType": "authorization_code"}
	for k, v := range want {
		if oauth.requests[0][k] != v {
			t.Errorf("exchange %s = %q, want %q", k, oauth.requests[0][k], v)
		}
	}
	if _, has := oauth.requests[0]["clientSecret"]; has {
		t.Error("the hosted exchange must not send a client secret")
	}
	if id := c.Identity(); id.UserID != "42" || id.CorpID != "ding1" {
		t.Fatalf("identity = %+v", id)
	}
	if !c.Alive() || len(gw.tools()) != 1 {
		t.Fatalf("alive=%v tools=%v", c.Alive(), gw.tools())
	}
}

func TestNewWithClientSecretUsesDingTalkOAuth(t *testing.T) {
	_, gwURL := meGateway(t)
	oauth, url := startOAuth(t, func(path string, body map[string]string) (int, string) {
		return 200, `{"accessToken":"uat-1","refreshToken":"rt-1","expireIn":7200,"corpId":"corp-1"}`
	})
	c, err := New(context.Background(), Config{GatewayURL: gwURL, TokenURL: url + "/token", ClientSecret: "secret"},
		AuthCode{Code: "code-1", ClientID: "client-1"})
	if err != nil {
		t.Fatal(err)
	}
	if tok := c.Token(); tok.AccessToken != "uat-1" || tok.RefreshToken != "rt-1" || tok.CorpID != "corp-1" {
		t.Fatalf("token = %v", tok)
	}
	if oauth.paths[0] != "/token" || oauth.requests[0]["clientSecret"] != "secret" || oauth.requests[0]["code"] != "code-1" {
		t.Fatalf("path=%s body=%v", oauth.paths[0], oauth.requests[0])
	}
}

func TestNewFailures(t *testing.T) {
	// A port nothing listens on: the connection is refused before sending.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closedURL := "http://" + l.Addr().String()
	l.Close()

	_, gwURL := meGateway(t)
	_, rejectAuth := startOAuth(t, func(string, map[string]string) (int, string) {
		// Measured on the staging endpoint with an unknown code.
		return 200, `{"errorCode":"invalidParameter.authCode.notFound","errorMsg":"不合法的临时授权码","success":false}`
	})
	_, downAuth := startOAuth(t, func(string, map[string]string) (int, string) { return 503, "busy" })
	_, wrongTokenAuth := startOAuth(t, func(string, map[string]string) (int, string) {
		return 200, `{"accessToken":"uat-other","expiresIn":7200}`
	})
	_, goodAuth := startOAuth(t, func(string, map[string]string) (int, string) {
		return 200, `{"accessToken":"uat-1","expiresIn":7200}`
	})
	_, garbledAuth := startOAuth(t, func(string, map[string]string) (int, string) { return 200, `<html>busy</html>` })

	cases := []struct {
		name      string
		cfg       Config
		code      AuthCode
		stage     InitStage
		kind      error
		spent     bool
		temporary bool
		provider  string
	}{
		{"bad env", Config{Env: "pre"}, AuthCode{Code: "c", ClientID: "id"}, StageConfig, ErrInvalidConfig, false, false, ""},
		{"empty code", Config{}, AuthCode{ClientID: "id"}, StageConfig, ErrInvalidConfig, false, false, ""},
		{"code rejected", Config{GatewayURL: gwURL, AuthURL: rejectAuth}, AuthCode{Code: "c", ClientID: "id"},
			StageExchange, ErrAuthCodeRejected, true, false, "invalidParameter.authCode.notFound"},
		{"exchange 503", Config{GatewayURL: gwURL, AuthURL: downAuth}, AuthCode{Code: "c", ClientID: "id"},
			StageExchange, ErrExchangeUnavailable, true, true, ""},
		{"unreadable 200", Config{GatewayURL: gwURL, AuthURL: garbledAuth}, AuthCode{Code: "c", ClientID: "id"},
			StageExchange, ErrExchangeUnavailable, true, true, ""},
		{"connection refused", Config{GatewayURL: gwURL, AuthURL: closedURL}, AuthCode{Code: "c", ClientID: "id"},
			StageExchange, ErrExchangeUnavailable, false, true, ""},
		{"token rejected at verify", Config{GatewayURL: gwURL, AuthURL: wrongTokenAuth}, AuthCode{Code: "c", ClientID: "id"},
			StageVerify, ErrIdentityUnverified, true, false, ""},
		{"gateway down at verify", Config{GatewayURL: closedURL, AuthURL: goodAuth}, AuthCode{Code: "c", ClientID: "id"},
			StageVerify, ErrIdentityUnverified, true, true, ""},
		{"someone else", Config{GatewayURL: gwURL, AuthURL: goodAuth}, AuthCode{Code: "c", ClientID: "id", ExpectUserID: "7"},
			StageVerify, ErrIdentityMismatch, true, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := New(context.Background(), tc.cfg, tc.code)
			var ie *InitError
			if c != nil || !errors.As(err, &ie) {
				t.Fatalf("client=%v err=%v, want *InitError", c, err)
			}
			if ie.Stage != tc.stage || !errors.Is(err, tc.kind) || ie.CodeSpent != tc.spent || ie.Temporary != tc.temporary || ie.Code != tc.provider {
				t.Fatalf("got stage=%s spent=%v temporary=%v code=%q err=%v", ie.Stage, ie.CodeSpent, ie.Temporary, ie.Code, err)
			}
			if strings.Contains(err.Error(), "uat-") {
				t.Fatalf("init error leaks a token: %v", err)
			}
		})
	}
}

func TestNewWithTokenFailureNeverSpendsACode(t *testing.T) {
	_, gwURL := meGateway(t)
	_, err := NewWithToken(context.Background(), Config{GatewayURL: gwURL}, Token{AccessToken: "wrong"})
	var ie *InitError
	if !errors.As(err, &ie) || ie.Stage != StageVerify || ie.CodeSpent || !errors.Is(err, ErrIdentityUnverified) {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewWithToken(context.Background(), Config{}, Token{AccessToken: "t", RefreshToken: "r"}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("refresh token without client id: %v", err)
	}
}
