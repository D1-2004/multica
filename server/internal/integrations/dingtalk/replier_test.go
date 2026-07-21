package dingtalk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeMinter struct{ lastUserID string }

func (f *fakeMinter) Mint(_ context.Context, _, _ pgtype.UUID, userID string) (BindingToken, error) {
	f.lastUserID = userID
	return BindingToken{Raw: "tok_raw"}, nil
}

// sessionWebhookRecorder captures webhook posts.
type sessionWebhookRecorder struct {
	mu     chan struct{}
	bodies []map[string]any
}

func newWebhookServer(t *testing.T) (*sessionWebhookRecorder, *httptest.Server) {
	rec := &sessionWebhookRecorder{mu: make(chan struct{}, 1)}
	rec.mu <- struct{}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		<-rec.mu
		rec.bodies = append(rec.bodies, body)
		rec.mu <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	return rec, srv
}

func inboundWithWebhook(t *testing.T, webhook string) channel.InboundMessage {
	t.Helper()
	msg, ok := inboundFromBotCallback(botCallbackData{
		ConversationID:   "cid",
		MsgID:            "m1",
		SenderStaffID:    "staff1",
		ConversationType: "1",
		Msgtype:          "text",
		SessionWebhook:   webhook,
	}, "ding_client")
	if !ok {
		t.Fatal("inbound mapping failed")
	}
	return msg
}

func TestReplierBindingPromptPostsSessionWebhook(t *testing.T) {
	rec, srv := newWebhookServer(t)
	minter := &fakeMinter{}
	r := NewOutboundReplier(OutboundReplierConfig{
		Binding: minter,
		AppURL:  "https://app.example",
	})
	msg := inboundWithWebhook(t, srv.URL)
	r.Reply(context.Background(), engine.ResolvedInstallation{}, msg, engine.Result{
		Outcome: engine.OutcomeNeedsBinding,
		Sender:  "staff1",
	})
	if minter.lastUserID != "staff1" {
		t.Errorf("minted for %q", minter.lastUserID)
	}
	if len(rec.bodies) != 1 {
		t.Fatalf("webhook posts = %d, want 1", len(rec.bodies))
	}
	body := rec.bodies[0]
	if body["msgtype"] != "markdown" {
		t.Errorf("msgtype = %v", body["msgtype"])
	}
	md, _ := body["markdown"].(map[string]any)
	text, _ := md["text"].(string)
	if !strings.Contains(text, "https://app.example/dingtalk/bind?token=tok_raw") {
		t.Errorf("binding prompt text = %q", text)
	}
}

func TestReplierBindingPromptUsesRobotAPIForHTTPCallback(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/v1.0/oauth2/accessToken":
			_, _ = w.Write([]byte(`{"accessToken":"tok_test","expireIn":7200}`))
		case "/v1.0/robot/oToMessages/batchSend":
			_ = json.NewDecoder(req.Body).Decode(&got)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected path %s", req.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	config, err := encodeInstallConfig(Installation{
		ClientID:           "ding-client",
		AppSecretEncrypted: []byte("client-secret"),
		RobotCode:          "robot-code",
		TransportMode:      TransportModeHTTPCallback,
		ConnectionManaged:  false,
	})
	if err != nil {
		t.Fatalf("encode install config: %v", err)
	}
	message, err := InboundFromHTTPCallback(HTTPCallbackMessage{
		ConversationID:   "cid-direct",
		ConversationType: "single",
		MessageID:        "msg-http-1",
		SenderID:         "open-sender",
		SenderStaffID:    "staff-1",
		Text:             "hello",
	}, "ding-client", "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("map HTTP callback: %v", err)
	}
	minter := &fakeMinter{}
	replier := NewOutboundReplier(OutboundReplierConfig{
		Binding:   minter,
		AppURL:    "https://app.example",
		Messenger: NewRobotMessenger(srv.URL, srv.URL, srv.Client()),
		Decrypt: func(ciphertext []byte) ([]byte, error) {
			return ciphertext, nil
		},
	})
	replier.Reply(context.Background(), engine.ResolvedInstallation{
		Platform: db.ChannelInstallation{Config: config},
	}, message, engine.Result{
		Outcome: engine.OutcomeNeedsBinding,
		Sender:  "open-sender",
	})

	if minter.lastUserID != "open-sender" {
		t.Fatalf("minted for %q, want route sender", minter.lastUserID)
	}
	users, _ := got["userIds"].([]any)
	if len(users) != 1 || users[0] != "staff-1" {
		t.Fatalf("robot API userIds = %#v", got["userIds"])
	}
	msgParam, _ := got["msgParam"].(string)
	if !strings.Contains(msgParam, "https://app.example/dingtalk/bind?token=tok_raw") {
		t.Fatalf("robot API msgParam = %q", msgParam)
	}
}

func TestReplierDoesNotFallbackStreamToRobotAPI(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	config, err := encodeInstallConfig(Installation{
		ClientID:           "ding-client",
		AppSecretEncrypted: []byte("client-secret"),
		RobotCode:          "robot-code",
		TransportMode:      TransportModeStream,
		ConnectionManaged:  true,
	})
	if err != nil {
		t.Fatalf("encode install config: %v", err)
	}
	message, err := InboundFromHTTPCallback(HTTPCallbackMessage{
		ConversationID:   "cid-direct",
		ConversationType: "single",
		MessageID:        "msg-stream-without-webhook",
		SenderID:         "open-sender",
		SenderStaffID:    "staff-1",
		Text:             "hello",
	}, "ding-client", "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("map callback: %v", err)
	}
	replier := NewOutboundReplier(OutboundReplierConfig{
		Messenger: NewRobotMessenger(srv.URL, srv.URL, srv.Client()),
		Decrypt: func(ciphertext []byte) ([]byte, error) {
			return ciphertext, nil
		},
	})
	err = replier.post(context.Background(), engine.ResolvedInstallation{
		Platform: db.ChannelInstallation{Config: config},
	}, message, "must stay on the Stream reply path")
	if err == nil || !strings.Contains(err.Error(), "stream inbound message carries no session webhook") {
		t.Fatalf("Stream fallback error = %v", err)
	}
	if called {
		t.Fatal("Stream install unexpectedly called the Robot API")
	}
}

func TestReplierIssueCreatedConfirmation(t *testing.T) {
	rec, srv := newWebhookServer(t)
	r := NewOutboundReplier(OutboundReplierConfig{AppURL: "https://app.example"})
	msg := inboundWithWebhook(t, srv.URL)
	r.Reply(context.Background(), engine.ResolvedInstallation{}, msg, engine.Result{
		Outcome:         engine.OutcomeIngested,
		IssueID:         pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		IssueIdentifier: "MUL-42",
		IssueTitle:      "修复登录",
	})
	if len(rec.bodies) != 1 {
		t.Fatalf("webhook posts = %d, want 1", len(rec.bodies))
	}
	md, _ := rec.bodies[0]["markdown"].(map[string]any)
	text, _ := md["text"].(string)
	if !strings.Contains(text, "MUL-42") || !strings.Contains(text, "修复登录") {
		t.Errorf("issue confirmation = %q", text)
	}
}

func TestReplierPlainIngestStaysSilent(t *testing.T) {
	rec, srv := newWebhookServer(t)
	r := NewOutboundReplier(OutboundReplierConfig{AppURL: "https://app.example"})
	msg := inboundWithWebhook(t, srv.URL)
	r.Reply(context.Background(), engine.ResolvedInstallation{}, msg, engine.Result{
		Outcome: engine.OutcomeIngested,
	})
	if len(rec.bodies) != 0 {
		t.Fatalf("webhook posts = %d, want 0 (agent reply lands via EventChatDone)", len(rec.bodies))
	}
}

func TestPostSessionWebhookSurfacesEnvelopeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":310000,"errmsg":"keywords not in content"}`))
	}))
	t.Cleanup(srv.Close)
	err := postSessionWebhook(context.Background(), srv.Client(), srv.URL, "hello")
	if err == nil || !strings.Contains(err.Error(), "errcode_310000") {
		t.Errorf("err = %v, want errcode envelope error", err)
	}
}
