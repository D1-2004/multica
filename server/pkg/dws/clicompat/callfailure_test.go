package clicompat

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/dws"
)

func TestCallFailureFromDWSRPCErrors(t *testing.T) {
	tests := []struct {
		name                     string
		err                      error
		category, reason, server string
		exit                     int
		legacy                   []string
	}{
		{name: "http 401", err: &dws.Error{Server: "chat", Tool: "t", Kind: dws.KindHTTP, Status: 401},
			category: "auth", reason: "http_401", exit: 2, legacy: []string{`"retryable": false`, `"dws doctor --json"`}},
		{name: "http 403", err: &dws.Error{Server: "chat", Tool: "t", Kind: dws.KindHTTP, Status: 403},
			category: "auth", reason: "http_403", exit: 2},
		{name: "http 400 missing token is api", err: &dws.Error{Server: "chat", Tool: "t", Kind: dws.KindHTTP, Status: 400, Message: "Missing service_id or access_key"},
			category: "api", reason: "http_400", exit: 1},
		{name: "http 503 retryable", err: &dws.Error{Server: "im", Tool: "t", Kind: dws.KindHTTP, Status: 503},
			category: "api", reason: "http_503", exit: 1, legacy: []string{`"retryable": true`, `"hint": "Upstream service error; retry later."`}},
		{name: "rpc invalid params", err: &dws.Error{Server: "chat", Tool: "t", Kind: dws.KindRPC, Code: "-32602", Message: "bad"},
			category: "validation", reason: "tools_call_jsonrpc_invalid_params", exit: 3, legacy: []string{`"rpc_code": -32602`}},
		{name: "rpc permission", err: &dws.Error{Server: "chat", Tool: "t", Kind: dws.KindRPC, Code: "-32000", Message: "Permission missing"},
			category: "auth", reason: "rpc_forbidden", exit: 2},
		{name: "rpc token", err: &dws.Error{Server: "chat", Tool: "t", Kind: dws.KindRPC, Code: "-32000", Message: "token expired"},
			category: "auth", reason: "tools_call_jsonrpc_server_error_32000", exit: 2},
		{name: "rpc invalid request is discovery", err: &dws.Error{Server: "chat", Tool: "t", Kind: dws.KindRPC, Code: "-32600", Message: "x"},
			category: "discovery", reason: "tools_call_jsonrpc_invalid_request", exit: 6},
		{name: "rpc other", err: &dws.Error{Server: "chat", Tool: "t", Kind: dws.KindRPC, Code: "12", Message: "x"},
			category: "api", reason: "tools_call_jsonrpc_error_12", exit: 1},
		{name: "tool error with json diag", err: &dws.Error{Server: "im", Tool: "t", Kind: dws.KindTool, Message: `{"errorCode":"A2UI_TARGET_INVALID","trace_id":"t9"}`},
			category: "api", reason: "mcp_tool_error", server: "im", exit: 1, legacy: []string{`"server_error_code": "A2UI_TARGET_INVALID"`, `"trace_id": "t9"`}},
		{name: "tool error PARAM_ERROR override", err: &dws.Error{Server: "chat", Tool: "t", Kind: dws.KindTool, Message: `{"errorCode":"PARAM_ERROR"}`},
			category: "api", reason: "invalid_request", server: "chat", exit: 1},
		{name: "payload auth code from CallRaw", err: &dws.Error{Server: "chat", Tool: "list_conversation_message_v2", Kind: dws.KindBusiness, Code: "USER_TOKEN_ILLEGAL"},
			category: "auth", exit: 2, legacy: []string{`"category": "internal"`, `"message": "[AUTH_TOKEN_EXPIRED] USER_TOKEN_ILLEGAL (operation: chat/list_conversation_message_v2)`}},
		{name: "business from Call", err: &dws.Error{Server: "chat", Tool: "t", Kind: dws.KindBusiness, Code: "1001", Message: "repeated"},
			category: "api", reason: "business_error", server: "chat", exit: 1, legacy: []string{`"server_error_code": "1001"`}},
		{name: "decode", err: &dws.Error{Server: "chat", Tool: "t", Kind: dws.KindDecode, Message: "invalid JSON-RPC response"},
			category: "discovery", reason: "tools_call_invalid_response", exit: 6},
		{name: "session expired wins over the wrapped gateway error",
			err:      fmt.Errorf("%w: %w", dws.ErrSessionExpired, &dws.Error{Server: "chat", Tool: "t", Kind: dws.KindHTTP, Status: 401}),
			category: "auth", reason: "auth_refresh_failed", exit: 2, legacy: []string{`"operation": "auth/token/refresh"`}},
		{name: "invalid request", err: fmt.Errorf("dws: empty text: %w", dws.ErrInvalidRequest), category: "validation", exit: 3},
		{name: "deadline", err: fmt.Errorf("dws: chat/t: %w", context.DeadlineExceeded),
			category: "api", reason: "request_timeout", exit: 1, legacy: []string{`"retryable": false`}},
		{name: "other error is plain internal", err: errors.New("boom"), category: "internal", exit: 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := CallFailure(tt.err)
			if f == nil || f.Category != tt.category || f.Reason != tt.reason || f.ExitCode != tt.exit || f.Server != tt.server {
				t.Fatalf("CallFailure = %+v", f)
			}
			legacy := string(f.LegacyJSON())
			for _, want := range tt.legacy {
				if !strings.Contains(legacy, want) {
					t.Fatalf("LegacyJSON lacks %s:\n%s", want, legacy)
				}
			}
		})
	}
	if CallFailure(nil) != nil {
		t.Fatal("nil error must map to nil")
	}
}

func TestCallFailureFromRealNetworkErrors(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedURL := "http://" + listener.Addr().String()
	_ = listener.Close()

	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer slow.Close()
	defer close(release)

	for _, tt := range []struct {
		name, url, reason string
		timeout           time.Duration
		retryable         bool
	}{
		{"connection refused", closedURL, "connection_refused", 5 * time.Second, true},
		{"deadline", slow.URL, "request_timeout", 50 * time.Millisecond, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client, err := dws.NewWithToken(context.Background(), dws.Config{GatewayURL: tt.url, SkipVerify: true},
				dws.Token{AccessToken: "token", ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), tt.timeout)
			defer cancel()
			_, callErr := client.CallRaw(ctx, dws.ServerChat, "list_conversation_message_v2", map[string]any{})
			f := CallFailure(callErr)
			if f == nil || f.Category != "api" || f.Reason != tt.reason || f.ExitCode != 1 {
				t.Fatalf("CallFailure(%v) = %+v", callErr, f)
			}
			if want := fmt.Sprintf(`"retryable": %v`, tt.retryable); !strings.Contains(string(f.LegacyJSON()), want) {
				t.Fatalf("LegacyJSON lacks %s:\n%s", want, f.LegacyJSON())
			}
			if !strings.HasPrefix(f.Message, "request to https://mcp-gw.dingtalk.com failed: Post ") {
				t.Fatalf("message = %q", f.Message)
			}
			unified := string(f.UnifiedJSON())
			if !strings.Contains(unified, `"stage": "request"`) || !strings.Contains(unified, `"subtype": "`+tt.reason+`"`) {
				t.Fatalf("unified = %s", unified)
			}
		})
	}
}
