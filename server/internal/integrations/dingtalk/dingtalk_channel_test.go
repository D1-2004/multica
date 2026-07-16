package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
)

// fakeStreamServer stands in for DingTalk's gateway + Stream endpoint: it
// serves /v1.0/gateway/connections/open and upgrades /stream?ticket=… to a
// WebSocket the test scripts frames onto.
type fakeStreamServer struct {
	t        *testing.T
	upgrader websocket.Upgrader

	mu       sync.Mutex
	gateway  []map[string]any // captured gateway open requests
	conn     *websocket.Conn
	acks     chan streamFrameResponse
	connOpen chan struct{}
}

type fakeStreamInbox struct {
	mu sync.Mutex

	admissions  []StreamFrameAdmission
	clientIDs   []string
	notifyCount int
	persistErr  error
	receipt     StreamInboxReceipt
	entered     chan struct{}
	release     chan struct{}
}

func (f *fakeStreamInbox) PersistFrame(
	ctx context.Context,
	clientID string,
	frame StreamFrameAdmission,
) (StreamInboxReceipt, error) {
	if f.entered != nil {
		select {
		case f.entered <- struct{}{}:
		default:
		}
	}
	if f.release != nil {
		select {
		case <-ctx.Done():
			return StreamInboxReceipt{}, ctx.Err()
		case <-f.release:
		}
	}
	if f.persistErr != nil {
		return StreamInboxReceipt{}, f.persistErr
	}
	f.mu.Lock()
	f.clientIDs = append(f.clientIDs, clientID)
	f.admissions = append(f.admissions, frame)
	receipt := f.receipt
	f.mu.Unlock()
	return receipt, nil
}

func (f *fakeStreamInbox) Notify() {
	f.mu.Lock()
	f.notifyCount++
	f.mu.Unlock()
}

func newFakeStreamServer(t *testing.T) (*fakeStreamServer, *httptest.Server) {
	f := &fakeStreamServer{
		t:        t,
		acks:     make(chan streamFrameResponse, 16),
		connOpen: make(chan struct{}, 1),
	}
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc(streamGatewayPath, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.gateway = append(f.gateway, body)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		endpoint := "ws" + strings.TrimPrefix(srv.URL, "http") + "/stream"
		_ = json.NewEncoder(w).Encode(map[string]string{"endpoint": endpoint, "ticket": "tk_test"})
	})
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ticket") != "tk_test" {
			http.Error(w, "bad ticket", http.StatusForbidden)
			return
		}
		conn, err := f.upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conn = conn
		f.mu.Unlock()
		f.connOpen <- struct{}{}
		// Read loop: collect ACK frames the client writes back.
		for {
			var resp streamFrameResponse
			if err := conn.ReadJSON(&resp); err != nil {
				return
			}
			f.acks <- resp
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv
}

// sendFrame scripts one frame from the "server" side.
func (f *fakeStreamServer) sendFrame(t *testing.T, frame streamFrame) {
	t.Helper()
	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	if conn == nil {
		t.Fatal("no websocket connection")
	}
	if err := conn.WriteJSON(frame); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

func (f *fakeStreamServer) waitAck(t *testing.T) streamFrameResponse {
	t.Helper()
	select {
	case ack := <-f.acks:
		return ack
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ack")
		return streamFrameResponse{}
	}
}

func (f *fakeStreamServer) assertNoAck(t *testing.T, wait time.Duration) {
	t.Helper()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case ack := <-f.acks:
		t.Fatalf("unexpected ACK: %+v", ack)
	case <-timer.C:
	}
}

// newTestChannel builds a dingtalkChannel against the fake server with a
// durable inbox stand-in.
func newTestChannel(t *testing.T, srvURL string, inbox StreamInbox) *dingtalkChannel {
	t.Helper()
	box, err := secretbox.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}
	sealed, err := box.Seal([]byte("s3cret"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	cfg, err := encodeInstallConfig(Installation{ClientID: "ding_client", AppSecretEncrypted: sealed})
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}
	factory := newDingTalkFactory(ChannelDeps{Decrypt: box.Open, OpenAPIBase: srvURL, Inbox: inbox})
	ch, err := factory(channel.Config{
		Type:           TypeDingtalk,
		Raw:            cfg,
		InstallationID: "00000000-0000-0000-0000-000000000020",
		ConnectionID:   "node-a-g1",
		NodeID:         "node-a",
	})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	return ch.(*dingtalkChannel)
}

func botCallbackFrame(t *testing.T, data botCallbackData) streamFrame {
	t.Helper()
	payload, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal callback: %v", err)
	}
	return streamFrame{
		SpecVersion: "1.0",
		Type:        streamFrameTypeCallback,
		Headers: map[string]string{
			streamHeaderTopic:       streamTopicBotMessage,
			streamHeaderMessageID:   "frame-1",
			streamHeaderContentType: streamContentTypeJSON,
		},
		Data: string(payload),
	}
}

func TestChannelConnectHandlesPingCallbackAndDisconnect(t *testing.T) {
	f, srv := newFakeStreamServer(t)
	inbox := &fakeStreamInbox{
		receipt: StreamInboxReceipt{
			ID:             "inbox-1",
			InstallationID: "installation-1",
			Status:         "queued",
			DeliveryCount:  1,
		},
	}
	ch := newTestChannel(t, srv.URL, inbox)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	connectErr := make(chan error, 1)
	go func() { connectErr <- ch.Connect(ctx) }()

	select {
	case <-f.connOpen:
	case <-time.After(5 * time.Second):
		t.Fatal("connection never opened")
	}

	// The gateway open carried the credentials + the bot-message topic.
	f.mu.Lock()
	gw := f.gateway[0]
	f.mu.Unlock()
	if gw["clientId"] != "ding_client" || gw["clientSecret"] != "s3cret" {
		t.Errorf("gateway credentials = %v", gw)
	}
	subs, _ := json.Marshal(gw["subscriptions"])
	if !strings.Contains(string(subs), streamTopicBotMessage) {
		t.Errorf("gateway subscriptions = %s", subs)
	}

	// SYSTEM ping → pong mirroring the data, echoing messageId.
	f.sendFrame(t, streamFrame{
		Type:    streamFrameTypeSystem,
		Headers: map[string]string{streamHeaderTopic: streamTopicPing, streamHeaderMessageID: "ping-1"},
		Data:    `{"ts": 123}`,
	})
	pong := f.waitAck(t)
	if pong.Code != streamAckCodeOK || pong.Headers[streamHeaderMessageID] != "ping-1" || pong.Data != `{"ts": 123}` {
		t.Errorf("pong = %+v", pong)
	}

	// CALLBACK bot message → durable admission, then ACK. Dispatch belongs to
	// the asynchronous inbox worker and is deliberately absent here.
	f.sendFrame(t, botCallbackFrame(t, botCallbackData{
		ConversationID:   "cidXXX==",
		MsgID:            "msg_1",
		SenderStaffID:    "staff_1",
		SenderNick:       "小明",
		SessionWebhook:   "https://oapi.dingtalk.com/robot/sendBySession?session=abc",
		ConversationType: "1",
		Msgtype:          "text",
		Text: struct {
			Content string `json:"content"`
		}{Content: " 你好 "},
	}))
	ack := f.waitAck(t)
	if ack.Code != streamAckCodeOK || ack.Headers[streamHeaderMessageID] != "frame-1" {
		t.Errorf("callback ack = %+v", ack)
	}
	waitFor(t, func() bool {
		inbox.mu.Lock()
		defer inbox.mu.Unlock()
		return len(inbox.admissions) == 1 && inbox.notifyCount == 1
	}, "inbox admission")
	inbox.mu.Lock()
	admission := inbox.admissions[0]
	clientID := inbox.clientIDs[0]
	inbox.mu.Unlock()
	if clientID != "ding_client" || admission.MessageID != "frame-1" || admission.Topic != streamTopicBotMessage {
		t.Errorf("admission route = client %q frame %+v", clientID, admission)
	}
	if admission.InstallationID != "00000000-0000-0000-0000-000000000020" || admission.ConnectionID != "node-a-g1" || admission.NodeID != "node-a" {
		t.Errorf("admission source metadata = %+v", admission)
	}
	if !strings.Contains(admission.Data, `"sessionWebhook"`) || !strings.Contains(admission.Data, `"msg_1"`) {
		t.Errorf("admission data did not preserve callback payload")
	}

	// SYSTEM disconnect → Connect returns an error so the supervisor
	// redials with a fresh gateway grant.
	f.sendFrame(t, streamFrame{
		Type:    streamFrameTypeSystem,
		Headers: map[string]string{streamHeaderTopic: streamTopicDisconnect, streamHeaderMessageID: "disc-1"},
	})
	select {
	case err := <-connectErr:
		if err == nil || !strings.Contains(err.Error(), "disconnect") {
			t.Errorf("Connect error = %v, want server-requested disconnect", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Connect did not return after disconnect frame")
	}
}

func TestChannelConnectReturnsNilOnContextCancel(t *testing.T) {
	f, srv := newFakeStreamServer(t)
	ch := newTestChannel(t, srv.URL, &fakeStreamInbox{})

	ctx, cancel := context.WithCancel(context.Background())
	connectErr := make(chan error, 1)
	go func() { connectErr <- ch.Connect(ctx) }()
	select {
	case <-f.connOpen:
	case <-time.After(5 * time.Second):
		t.Fatal("connection never opened")
	}
	cancel()
	select {
	case err := <-connectErr:
		if err != nil {
			t.Errorf("Connect after cancel = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Connect did not return after cancel")
	}
}

func TestChannelDoesNotAckWhenInboxCommitFails(t *testing.T) {
	f, srv := newFakeStreamServer(t)
	infra := errors.New("db down")
	ch := newTestChannel(t, srv.URL, &fakeStreamInbox{persistErr: infra})
	var logs strings.Builder
	ch.logger = slog.New(slog.NewJSONHandler(&logs, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	connectErr := make(chan error, 1)
	go func() { connectErr <- ch.Connect(ctx) }()
	select {
	case <-f.connOpen:
	case <-time.After(5 * time.Second):
		t.Fatal("connection never opened")
	}
	f.sendFrame(t, botCallbackFrame(t, botCallbackData{
		ConversationID: "cid", MsgID: "m1", SenderStaffID: "s1", ConversationType: "2", Msgtype: "text",
	}))
	select {
	case err := <-connectErr:
		if !errors.Is(err, infra) {
			t.Errorf("Connect error = %v, want inbox commit error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Connect did not propagate inbox commit error")
	}
	f.assertNoAck(t, 150*time.Millisecond)
	for _, field := range []string{
		"dingtalk_stream_callback_admission_failed",
		"00000000-0000-0000-0000-000000000020",
		"node-a-g1",
		"node-a",
		streamInboxTraceHash("frame-1"),
	} {
		if !strings.Contains(logs.String(), field) {
			t.Errorf("admission failure logs missing %q: %s", field, logs.String())
		}
	}
}

func TestChannelCommitsBeforeCallbackAck(t *testing.T) {
	f, srv := newFakeStreamServer(t)
	inbox := &fakeStreamInbox{
		receipt: StreamInboxReceipt{InstallationID: "installation-1", Status: "queued", DeliveryCount: 1},
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	ch := newTestChannel(t, srv.URL, inbox)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	connectErr := make(chan error, 1)
	go func() { connectErr <- ch.Connect(ctx) }()
	select {
	case <-f.connOpen:
	case <-time.After(5 * time.Second):
		t.Fatal("connection never opened")
	}
	f.sendFrame(t, botCallbackFrame(t, botCallbackData{
		ConversationID: "cid", MsgID: "m1", SenderStaffID: "s1", ConversationType: "2", Msgtype: "text",
	}))
	select {
	case <-inbox.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("callback did not enter inbox admission")
	}
	f.assertNoAck(t, 150*time.Millisecond)
	close(inbox.release)
	if ack := f.waitAck(t); ack.Code != streamAckCodeOK {
		t.Errorf("ack = %+v", ack)
	}
}

func TestChannelMarksReadyBeforeReadingFirstFrame(t *testing.T) {
	f, srv := newFakeStreamServer(t)
	ch := newTestChannel(t, srv.URL, &fakeStreamInbox{})
	readyEntered := make(chan struct{}, 1)
	readyRelease := make(chan struct{})
	ch.onReady = func(ctx context.Context) error {
		readyEntered <- struct{}{}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-readyRelease:
			return nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	connectErr := make(chan error, 1)
	go func() { connectErr <- ch.Connect(ctx) }()
	select {
	case <-f.connOpen:
	case <-time.After(5 * time.Second):
		t.Fatal("connection never opened")
	}
	select {
	case <-readyEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("ready barrier was not entered")
	}

	f.sendFrame(t, streamFrame{
		Type:    streamFrameTypeSystem,
		Headers: map[string]string{streamHeaderTopic: streamTopicPing, streamHeaderMessageID: "ping-before-ready"},
		Data:    `{"ts": 1}`,
	})
	f.assertNoAck(t, 150*time.Millisecond)
	close(readyRelease)
	if ack := f.waitAck(t); ack.Headers[streamHeaderMessageID] != "ping-before-ready" {
		t.Fatalf("pong after ready = %+v", ack)
	}
	cancel()
	select {
	case err := <-connectErr:
		if err != nil {
			t.Fatalf("Connect after cancel = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Connect did not stop")
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
