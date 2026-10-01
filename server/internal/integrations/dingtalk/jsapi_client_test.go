package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newJSAPITestServer(t *testing.T, ticketBody string) (*httptest.Server, *int32, *int32) {
	t.Helper()
	var ticketCalls, convertCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1.0/oauth2/accessToken":
			_, _ = w.Write([]byte(`{"accessToken":"corp-token","expireIn":7200}`))
		case r.URL.Path == "/get_jsapi_ticket":
			atomic.AddInt32(&ticketCalls, 1)
			if r.URL.Query().Get("access_token") != "corp-token" {
				t.Errorf("ticket access_token=%q", r.URL.Query().Get("access_token"))
			}
			_, _ = w.Write([]byte(ticketBody))
		case strings.HasPrefix(r.URL.Path, "/v1.0/im/chat/") && strings.HasSuffix(r.URL.Path, "/convertToOpenConversationId"):
			atomic.AddInt32(&convertCalls, 1)
			if r.Method != http.MethodPost || r.Header.Get("x-acs-dingtalk-access-token") != "corp-token" {
				t.Errorf("convert method=%s token=%q", r.Method, r.Header.Get("x-acs-dingtalk-access-token"))
			}
			chatID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1.0/im/chat/"), "/convertToOpenConversationId")
			if chatID == "chat-unknown" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":"invalidParameter"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"openConversationId": "cid-" + chatID})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, &ticketCalls, &convertCalls
}

func TestClientJSAPITicketIsCached(t *testing.T) {
	server, ticketCalls, _ := newJSAPITestServer(t, `{"errcode":0,"errmsg":"ok","ticket":"ticket-1","expires_in":7200}`)
	client := NewClient(Config{AppKey: "k", AppSecret: "s", OpenAPIBase: server.URL, OAPIBase: server.URL})
	if !client.JSAPISupported() {
		t.Fatal("configured direct client must support JSAPI")
	}
	for i := 0; i < 2; i++ {
		ticket, err := client.JSAPITicket(context.Background())
		if err != nil || ticket != "ticket-1" {
			t.Fatalf("ticket=%q err=%v", ticket, err)
		}
	}
	if got := atomic.LoadInt32(ticketCalls); got != 1 {
		t.Fatalf("ticket endpoint calls=%d, want 1 (cached)", got)
	}
}

func TestClientJSAPITicketRefreshesNearExpiry(t *testing.T) {
	// expires_in shorter than the refresh margin is never served from cache.
	server, ticketCalls, _ := newJSAPITestServer(t, `{"errcode":0,"ticket":"short","expires_in":60}`)
	client := NewClient(Config{AppKey: "k", AppSecret: "s", OpenAPIBase: server.URL, OAPIBase: server.URL})
	for i := 0; i < 2; i++ {
		if _, err := client.JSAPITicket(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt32(ticketCalls); got != 2 {
		t.Fatalf("ticket endpoint calls=%d, want 2", got)
	}
}

func TestClientJSAPITicketRejectsErrorEnvelope(t *testing.T) {
	server, _, _ := newJSAPITestServer(t, `{"errcode":40001,"errmsg":"invalid token"}`)
	client := NewClient(Config{AppKey: "k", AppSecret: "s", OpenAPIBase: server.URL, OAPIBase: server.URL})
	_, err := client.JSAPITicket(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "40001" {
		t.Fatalf("err=%v, want APIError 40001", err)
	}
}

func TestClientConvertChatIDToOpenConversationID(t *testing.T) {
	server, _, convertCalls := newJSAPITestServer(t, `{}`)
	client := NewClient(Config{AppKey: "k", AppSecret: "s", OpenAPIBase: server.URL, OAPIBase: server.URL})
	cid, err := client.ConvertChatIDToOpenConversationID(context.Background(), " chat123 ")
	if err != nil || cid != "cid-chat123" {
		t.Fatalf("cid=%q err=%v", cid, err)
	}
	if _, err := client.ConvertChatIDToOpenConversationID(context.Background(), "chat-unknown"); err == nil {
		t.Fatal("DingTalk 4xx must surface as an error")
	}
	if _, err := client.ConvertChatIDToOpenConversationID(context.Background(), "  "); err == nil {
		t.Fatal("empty chatId must be rejected before any request")
	}
	if got := atomic.LoadInt32(convertCalls); got != 2 {
		t.Fatalf("convert calls=%d, want 2", got)
	}
}

func TestClientJSAPITicketTransportErrorHidesAccessToken(t *testing.T) {
	tokenServer, _, _ := newJSAPITestServer(t, `{"errcode":0,"ticket":"unused","expires_in":7200}`)
	// A closed server makes the ticket request fail in the transport, where
	// net/http reports the full request URL (with ?access_token=).
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	client := NewClient(Config{AppKey: "k", AppSecret: "s", OpenAPIBase: tokenServer.URL, OAPIBase: closed.URL})
	_, err := client.JSAPITicket(context.Background())
	if err == nil {
		t.Fatal("ticket request against a closed server must fail")
	}
	if strings.Contains(err.Error(), "corp-token") || strings.Contains(err.Error(), "access_token") {
		t.Fatalf("transport error leaked the access token: %v", err)
	}
	if !strings.Contains(err.Error(), "/get_jsapi_ticket") {
		t.Fatalf("redacted error lost the endpoint path: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceledClient := NewClient(Config{AppKey: "k", AppSecret: "s", OpenAPIBase: tokenServer.URL, OAPIBase: tokenServer.URL})
	canceledClient.token = tokenCache{value: "corp-token", expiresAt: time.Now().Add(time.Hour)}
	if _, err := canceledClient.JSAPITicket(ctx); !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "corp-token") {
		t.Fatalf("canceled ticket request err=%v, want a redacted context.Canceled", err)
	}
}

func TestRedactURLErrorLeavesOtherErrorsAlone(t *testing.T) {
	plain := errors.New("plain")
	if RedactURLError(plain) != plain || RedactURLError(nil) != nil {
		t.Fatal("non-URL errors must pass through unchanged")
	}
	redacted := RedactURLError(&url.Error{Op: "Get", URL: "https://user:pw@oapi.example/get_jsapi_ticket?access_token=secret#frag", Err: errors.New("boom")})
	if got := redacted.Error(); strings.Contains(got, "secret") || strings.Contains(got, "pw") || !strings.Contains(got, "boom") {
		t.Fatalf("redacted error = %q", got)
	}
}

func TestUnconfiguredAndAgentClientsDoNotSupportJSAPI(t *testing.T) {
	unconfigured := NewClient(Config{})
	if unconfigured.JSAPISupported() {
		t.Fatal("unconfigured client must not claim JSAPI support")
	}
	if _, err := unconfigured.JSAPITicket(context.Background()); err == nil {
		t.Fatal("unconfigured client must fail")
	}
	agent := NewAgentClient(AgentClientConfig{BaseURL: "https://agent.example.test", InternalSecret: "secret"})
	if agent.JSAPISupported() {
		t.Fatal("private agent client must not claim JSAPI support")
	}
	if _, err := agent.JSAPITicket(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("agent ticket err=%v, want ErrUnsupported", err)
	}
	if _, err := agent.ConvertChatIDToOpenConversationID(context.Background(), "chat"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("agent convert err=%v, want ErrUnsupported", err)
	}
}
