package dws

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCallSendsOneStatelessToolCall(t *testing.T) {
	g, url := startGateway(t, func(string, map[string]any, string) (int, string) { return ok(`{"openMessageId":"m1"}`) })
	c, _ := NewWithToken(context.Background(), Config{GatewayURL: url, SkipVerify: true, Header: http.Header{"X-Dingtalk-Trace-Id": {"trace-1"}}},
		Token{AccessToken: "tok-1"})
	raw, err := c.Call(context.Background(), ServerChat, "send_personal_message", map[string]any{"a": "b"})
	if err != nil || string(raw) != `{"openMessageId":"m1"}` {
		t.Fatalf("raw=%s err=%v", raw, err)
	}
	call := g.call("send_personal_message")
	if call.Server != "chat" || call.Args["a"] != "b" {
		t.Fatalf("call = %+v", call)
	}
	for header, want := range map[string]string{
		"x-user-access-token": "tok-1", "Accept": "application/json", "Content-Type": "application/json", "X-Dingtalk-Trace-Id": "trace-1",
	} {
		if got := call.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	if call.Header.Get("Authorization") != "" {
		t.Error("the gateway authenticates x-user-access-token; Authorization must not be sent")
	}
}

func TestCallErrorLayers(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		kind   ErrorKind
		code   string
		auth   bool
	}{
		{"missing token", 400, `{"error":"Missing service_id or access_key"}`, KindHTTP, "", true},
		{"unauthorized", 401, `nope`, KindHTTP, "", true},
		{"rpc error", 200, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"bad params"}}`, KindRPC, "-32602", false},
		{"tool error", 200, `{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"boom"}]}}`, KindTool, "", false},
		{"business string code", 200, toolText(`{"success":false,"errorCode":"MSG_CONTENT_TOO_LONG","errorMsg":"too long"}`), KindBusiness, "MSG_CONTENT_TOO_LONG", false},
		{"business numeric code", 200, toolText(`{"success":false,"errorCode":1001,"errorMsg":"Request is repeated"}`), KindBusiness, "1001", false},
		{"business auth code", 200, toolText(`{"success":false,"errorCode":"USER_TOKEN_ILLEGAL"}`), KindBusiness, "USER_TOKEN_ILLEGAL", true},
		{"not JSON-RPC", 200, `<html>`, KindDecode, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, c := newTestClient(t, func(string, map[string]any) (int, string) { return tc.status, tc.body })
			_, err := c.Call(context.Background(), ServerIM, "x", nil)
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if e.Kind != tc.kind || e.Code != tc.code || IsAuth(err) != tc.auth {
				t.Fatalf("got kind=%s code=%q auth=%v (%v)", e.Kind, e.Code, IsAuth(err), err)
			}
			// A token-only client cannot recover from a rejection.
			if expired := errors.Is(err, ErrSessionExpired); expired != tc.auth || c.Alive() == tc.auth {
				t.Fatalf("expired=%v alive=%v for auth=%v", expired, c.Alive(), tc.auth)
			}
		})
	}
}

func TestCallPassesThroughPayloadsWithoutEnvelope(t *testing.T) {
	_, c := newTestClient(t, func(tool string, _ map[string]any) (int, string) {
		if tool == "plain" {
			return 200, toolText("hello")
		}
		return 200, toolText(`{"userId":["103262"]}`)
	})
	if raw, err := c.Call(context.Background(), ServerContact, "search_user_by_key_word", nil); err != nil || string(raw) != `{"userId":["103262"]}` {
		t.Fatalf("raw=%s err=%v", raw, err)
	}
	if raw, err := c.Call(context.Background(), ServerContact, "plain", nil); err != nil || string(raw) != `"hello"` {
		t.Fatalf("raw=%s err=%v", raw, err)
	}
}

func TestErrorsNeverEchoTheToken(t *testing.T) {
	const secret = "secret-token-0123456789"
	for _, body := range []string{`{"error":"invalid token ` + secret + `"}`, `{"error":"` + strings.Repeat("x", 290) + secret + `"}`} {
		_, url := startGateway(t, func(string, map[string]any, string) (int, string) { return 400, body })
		c, _ := NewWithToken(context.Background(), Config{GatewayURL: url, SkipVerify: true}, Token{AccessToken: secret})
		_, err := c.Call(context.Background(), ServerIM, "x", nil)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("err = %v", err)
		}
	}
}

func TestCallNeverFollowsRedirects(t *testing.T) {
	leaked := false
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("x-user-access-token") != ""
	}))
	defer elsewhere.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer gateway.Close()
	c, _ := NewWithToken(context.Background(), Config{GatewayURL: gateway.URL, SkipVerify: true, HTTP: &http.Client{}}, Token{AccessToken: "tok-1"})
	_, err := c.Call(context.Background(), ServerIM, "x", nil)
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusTemporaryRedirect || leaked {
		t.Fatalf("err=%v leaked=%v", err, leaked)
	}
}

func TestConfigEndpoints(t *testing.T) {
	if (Config{}).gatewayURL() != "https://mcp-gw.dingtalk.com" || (Config{Env: EnvStaging}).gatewayURL() != "https://pre-mcp-gw.dingtalk.com" {
		t.Fatal("gateway URLs")
	}
	if (Config{}).authURL() != "https://mcp.dingtalk.com" || (Config{Env: EnvStaging}).authURL() != "https://pre-mcp.dingtalk.com" {
		t.Fatal("auth URLs")
	}
}

// CallRaw hands back the whole payload, a business failure included, and
// still refreshes and retries a token the payload says was refused.
func TestCallRawKeepsTheWholePayload(t *testing.T) {
	_, c := newTestClient(t, func(tool string, _ map[string]any) (int, string) {
		switch tool {
		case "refused":
			return 200, toolText(`{"success":false,"errorCode":"USER_TOKEN_ILLEGAL"}`)
		case "failed":
			return 200, toolText(`{"success":false,"errorCode":"130003","errorMsg":"OpenId is not in conversation","traceId":"t1"}`)
		default:
			return 200, toolText(`{"success":true,"result":{"openTaskId":"task-1"},"code":0}`)
		}
	})
	raw, err := c.CallRaw(context.Background(), ServerChat, "send", nil)
	if err != nil || string(raw) != `{"success":true,"result":{"openTaskId":"task-1"},"code":0}` {
		t.Fatalf("success payload = %s, %v", raw, err)
	}
	raw, err = c.CallRaw(context.Background(), ServerChat, "failed", nil)
	if err != nil || !strings.Contains(string(raw), `"errorCode":"130003"`) {
		t.Fatalf("business failure = %s, %v", raw, err)
	}
	// Without a refresh token the rejection ends the session instead of
	// coming back as a payload.
	if _, err := c.CallRaw(context.Background(), ServerChat, "refused", nil); !IsAuth(err) || !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("refused token: %v", err)
	}
}
