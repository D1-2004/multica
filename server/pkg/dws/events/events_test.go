package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/multica-ai/multica/server/pkg/dws"
)

// fakeDWS is the MCP host (ticket + subscription control plane) and the
// stream endpoint in one server. Each accepted connection runs script(n).
type fakeDWS struct {
	t   *testing.T
	srv *httptest.Server

	mu          sync.Mutex
	tickets     int
	ticketFails int // the next N ticket requests fail with 500
	subs        map[string]dws.Subscription
	byKey       map[string]string // idempotencyKey -> subId, kept after cancel
	nextSub     int
	cancelled   []string
	cancelFails int // the next N cancel requests fail with 500
	conns       atomic.Int32
	script      func(n int, c *websocket.Conn)
	// validToken is the token the control plane accepts (default "uat");
	// others get a business-code rejection inside HTTP 200.
	validToken string
	// endpoint overrides the ticket's endpoint.
	endpoint string
}

func newFakeDWS(t *testing.T, script func(n int, c *websocket.Conn)) *fakeDWS {
	f := &fakeDWS{t: t, subs: map[string]dws.Subscription{}, byKey: map[string]string{}, script: script, validToken: "uat"}
	upgrader := websocket.Upgrader{}
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, result any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result})
	}
	mux.HandleFunc("/oauth2/refreshToken", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"accessToken":%q,"refreshToken":"rt-2","expiresIn":7200}`, f.validToken)
	})
	mux.HandleFunc("/stream/connections/ticket", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("x-user-access-token") != f.validToken {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errorCode": "USER_TOKEN_ILLEGAL", "errorMsg": "token expired"})
			return
		}
		if r.Header.Get("X-DWS-Client-Id") != "client-1" || r.Header.Get("X-DWS-Source-Id") != "open" {
			t.Errorf("ticket headers %v", r.Header)
		}
		if f.ticketFails > 0 {
			f.ticketFails--
			w.WriteHeader(500)
			return
		}
		f.tickets++
		endpoint := f.endpoint
		if endpoint == "" {
			endpoint = "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/connect"
		}
		ok(w, map[string]string{"endpoint": endpoint, "ticket": fmt.Sprintf("secret-ticket-%d", f.tickets)})
	})
	mux.HandleFunc("/dws/subscription/user", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		key, _ := body["ext"].(map[string]any)["idempotencyKey"].(string)
		f.mu.Lock()
		id, seen := f.byKey[key]
		if !seen {
			f.nextSub++
			id = fmt.Sprintf("sub-%d", f.nextSub)
			f.byKey[key] = id
		}
		// Like DWS: the same key returns the same id, revived if cancelled.
		f.subs[id] = dws.Subscription{ID: id, EventKey: body["eventKey"].(string), Status: 1}
		f.mu.Unlock()
		ok(w, []string{id})
	})
	mux.HandleFunc("/dws/event/sublist", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		items := make([]dws.Subscription, 0, len(f.subs))
		for _, s := range f.subs {
			items = append(items, s)
		}
		f.mu.Unlock()
		ok(w, map[string]any{"total": len(items), "items": items})
	})
	mux.HandleFunc("/dws/subscription/cancel", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		if f.cancelFails > 0 {
			f.cancelFails--
			f.mu.Unlock()
			w.WriteHeader(500)
			return
		}
		delete(f.subs, body["subId"])
		f.cancelled = append(f.cancelled, body["subId"])
		f.mu.Unlock()
		ok(w, true)
	})
	mux.HandleFunc("/connect", func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		n := int(f.conns.Add(1))
		f.script(n, c)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDWS) client(t *testing.T) *dws.Client {
	c, err := dws.NewWithToken(context.Background(), dws.Config{AuthURL: f.srv.URL, GatewayURL: f.srv.URL, SkipVerify: true},
		dws.Token{AccessToken: "uat", ClientID: "client-1"})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type ack struct {
	Code    int               `json:"code"`
	Headers map[string]string `json:"headers"`
	Message string            `json:"message"`
	Data    string            `json:"data"`
}

func sendEvent(t *testing.T, c *websocket.Conn, id, key string, body map[string]any) {
	inner, _ := json.Marshal(map[string]any{"eventId": id, "eventKey": key, "subId": "sub-1", "occurredAtMs": 1790000000000,
		"payload": map[string]any{"corpid": "ding1", "body": body}})
	doubled, _ := json.Marshal(string(inner)) // data arrives JSON-encoded twice
	if err := c.WriteJSON(map[string]any{"specVersion": "1.0", "type": "EVENT",
		"headers": map[string]string{"messageId": "frame-" + id, "eventId": id, "TOPIC": "*"}, "data": string(doubled)}); err != nil {
		t.Error(err)
	}
}

func readAck(c *websocket.Conn) (ack, error) {
	var a ack
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	err := c.ReadJSON(&a)
	return a, err
}

var fast = Options{MinBackoff: 10 * time.Millisecond, MaxBackoff: 40 * time.Millisecond, StableAfter: 20 * time.Millisecond, Holder: "test"}

func TestListenerHandlesAcksDedupesAndReconnects(t *testing.T) {
	var handled []Event
	var mu sync.Mutex
	failOnce := true
	done := make(chan struct{})
	f := newFakeDWS(t, func(n int, c *websocket.Conn) {
		switch n {
		case 1:
			_ = c.WriteJSON(map[string]any{"type": "SYSTEM", "headers": map[string]string{"messageId": "p1", "topic": "ping"}, "data": `{"opaque":1}`})
			if a, _ := readAck(c); a.Code != 200 || a.Data != `{"opaque":1}` || a.Headers["messageId"] != "p1" {
				t.Errorf("pong = %+v", a)
			}
			body := map[string]any{"openConversationId": "cid-1", "openMessageId": "m1", "senderOpenDingTalkId": "s1", "content": "上线了"}
			sendEvent(t, c, "e1", dws.EventIMGroup, body)
			if a, _ := readAck(c); a.Code != 200 || a.Headers["messageId"] != "frame-e1" || !strings.Contains(a.Data, "SUCCESS") {
				t.Errorf("event ack = %+v", a)
			}
			sendEvent(t, c, "e1", dws.EventIMGroup, body) // redelivery
			if a, _ := readAck(c); a.Code != 200 {
				t.Errorf("duplicate ack = %+v", a)
			}
			_ = c.WriteJSON(map[string]any{"type": "SYSTEM", "headers": map[string]string{"messageId": "d1", "topic": "disconnect"}})
			_, _ = readAck(c)
		case 2:
			sendEvent(t, c, "e2", dws.EventIMGroup, map[string]any{"openConversationId": "cid-1"})
			if _, err := readAck(c); err == nil {
				t.Error("a failed event must not be acked")
			}
		case 3:
			sendEvent(t, c, "e2", dws.EventIMGroup, map[string]any{"openConversationId": "cid-1"}) // redelivered
			if a, err := readAck(c); err != nil || a.Code != 200 {
				t.Errorf("redelivered ack = %+v %v", a, err)
			}
			close(done)
			time.Sleep(time.Second)
		}
	})
	store := &MemoryStore{}
	l := &Listener{
		Identity: "agent-a", Store: store, Options: fast,
		Client:        func(context.Context) (*dws.Client, error) { return f.client(t), nil },
		Subscriptions: []dws.SubscriptionSpec{{EventKey: dws.EventIMGroup, ConversationID: "cid-1"}},
		Handle: func(_ context.Context, ev Event) error {
			mu.Lock()
			defer mu.Unlock()
			if ev.ID == "e2" && failOnce {
				failOnce = false
				return errors.New("database down")
			}
			handled = append(handled, ev)
			return nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = l.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
	cancel()
	mu.Lock()
	defer mu.Unlock()
	if len(handled) != 2 || handled[0].ID != "e1" || handled[1].ID != "e2" {
		t.Fatalf("handled = %+v", handled)
	}
	e := handled[0]
	if e.Key != dws.EventIMGroup || e.ConversationID != "cid-1" || e.MessageID != "m1" || e.Content != "上线了" || e.CorpID != "ding1" || e.SubscriptionID != "sub-1" {
		t.Fatalf("decoded = %+v", e)
	}
	if f.tickets < 3 {
		t.Fatalf("tickets = %d; every connection needs a fresh one", f.tickets)
	}
}

func TestListenerReconcilesSubscriptions(t *testing.T) {
	f := newFakeDWS(t, func(int, *websocket.Conn) { time.Sleep(time.Second) })
	store := &MemoryStore{}
	c := f.client(t)
	group := dws.SubscriptionSpec{EventKey: dws.EventIMGroup, ConversationID: "cid-1"}
	at := dws.SubscriptionSpec{EventKey: dws.EventIMAt}
	l := &Listener{Identity: "agent-a", Store: store, Subscriptions: []dws.SubscriptionSpec{group, at}}
	var st Status
	if err := l.reconcile(context.Background(), c, &st); err != nil || st.Subscriptions != 2 || len(f.subs) != 2 {
		t.Fatalf("err=%v st=%+v subs=%v", err, st, f.subs)
	}
	// Reconciling again changes nothing.
	_ = l.reconcile(context.Background(), c, &st)
	if f.nextSub != 2 {
		t.Fatalf("created %d, want 2", f.nextSub)
	}
	// The @ subscription is dropped from the config; the group one vanished
	// on DingTalk's side.
	stored, _ := store.Subscriptions(context.Background(), "agent-a")
	var groupID, atID string
	for _, s := range stored {
		if s.EventKey == dws.EventIMGroup {
			groupID = s.ID
		} else {
			atID = s.ID
		}
	}
	f.mu.Lock()
	delete(f.subs, groupID)
	f.mu.Unlock()
	l.Subscriptions = []dws.SubscriptionSpec{group}
	if err := l.reconcile(context.Background(), c, &st); err != nil {
		t.Fatal(err)
	}
	if len(f.cancelled) != 1 || f.cancelled[0] != atID || f.nextSub != 2 {
		t.Fatalf("cancelled=%v created=%d", f.cancelled, f.nextSub)
	}
	stored, _ = store.Subscriptions(context.Background(), "agent-a")
	if len(stored) != 1 || stored[0].ID != groupID || f.subs[groupID].Status != 1 {
		t.Fatalf("stored = %+v; the same id comes back active", stored)
	}
}

func TestListenerAlertsOnOutageAndRecovery(t *testing.T) {
	connected := make(chan struct{}, 1)
	f := newFakeDWS(t, func(int, *websocket.Conn) {
		connected <- struct{}{}
		time.Sleep(2 * time.Second)
	})
	f.ticketFails = 8
	var mu sync.Mutex
	var alerts []Alert
	opts := fast
	opts.AlertAfter = 50 * time.Millisecond
	opts.Alert = func(_ context.Context, a Alert) { mu.Lock(); alerts = append(alerts, a); mu.Unlock() }
	store := &MemoryStore{}
	l := &Listener{Identity: "agent-a", Store: store, Options: opts,
		Client:        func(context.Context) (*dws.Client, error) { return f.client(t), nil },
		Subscriptions: []dws.SubscriptionSpec{{EventKey: dws.EventIMAt}},
		Handle:        func(context.Context, Event) error { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = l.Run(ctx) }()
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("never connected")
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(alerts) != 2 || alerts[0].Kind != "down" || !strings.Contains(alerts[0].Err, "ticket") || alerts[1].Kind != "recovered" {
		t.Fatalf("alerts = %+v", alerts)
	}
	sts, _ := store.Statuses(context.Background())
	if len(sts) != 1 || sts[0].State != StateConnected || sts[0].Failures != 0 || sts[0].Alerting {
		t.Fatalf("status = %+v", sts)
	}
}

func TestManagerFailsOverAndRetires(t *testing.T) {
	f := newFakeDWS(t, func(_ int, c *websocket.Conn) {
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	})
	store := &MemoryStore{}
	var specsMu sync.Mutex
	specs := []Spec{{Identity: "agent-a", Subscriptions: []dws.SubscriptionSpec{{EventKey: dws.EventIMAt}}}}
	newManager := func(holder string) *Manager {
		o := fast
		o.Holder = holder
		return &Manager{Store: store, Tick: 20 * time.Millisecond, LeaseTTL: 100 * time.Millisecond, Options: o,
			Specs: func(context.Context) ([]Spec, error) {
				specsMu.Lock()
				defer specsMu.Unlock()
				return append([]Spec(nil), specs...), nil
			},
			Client: func(context.Context, string) (*dws.Client, error) { return f.client(t), nil },
			Handle: func(context.Context, string, Event) error { return nil }}
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	holder := func() string {
		sts, _ := store.Statuses(context.Background())
		for _, st := range sts {
			if st.Identity == "agent-a" && st.State == StateConnected {
				return st.Holder
			}
		}
		return ""
	}

	ctxA, stopA := context.WithCancel(context.Background())
	ctxB, stopB := context.WithCancel(context.Background())
	defer stopB()
	go func() { _ = newManager("A").Run(ctxA) }()
	waitFor("A to connect", func() bool { return holder() == "A" })
	go func() { _ = newManager("B").Run(ctxB) }()
	time.Sleep(150 * time.Millisecond)
	if got := f.conns.Load(); got != 1 {
		t.Fatalf("connections = %d; B must not connect while A holds the lease", got)
	}
	stopA() // a replica goes away: B takes over
	waitFor("B to take over", func() bool { return holder() == "B" })

	specsMu.Lock()
	specs = nil // the product page removes the identity
	specsMu.Unlock()
	waitFor("subscriptions to be cancelled", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.subs) == 0 && len(f.cancelled) == 1
	})
	waitFor("status stopped", func() bool {
		sts, _ := store.Statuses(context.Background())
		return len(sts) == 1 && sts[0].State == StateStopped
	})
}

func TestDecodeEvent(t *testing.T) {
	data, _ := json.Marshal(map[string]any{"eventKey": "user_im_message_reaction_group", "payload": map[string]any{
		"body": map[string]any{"openConversationId": "cid", "openSourceMessageId": "m9", "emotionName": "OK"}}})
	ev, err := decodeEvent(frame{Type: "EVENT", Headers: map[string]any{"MESSAGE_ID": "x1", "TOPIC": "*"}, Data: string(data)})
	if err != nil || ev.ID != "x1" || ev.Key != dws.EventIMReactionGroup || ev.MessageID != "m9" {
		t.Fatalf("ev=%+v err=%v", ev, err)
	}
	// The event's own id, even from the payload, wins over the frame's
	// messageId, which may change between deliveries.
	ev, err = decodeEvent(frame{Type: "EVENT", Headers: map[string]any{"messageId": "frame-7", "TOPIC": "*"}, Data: string(data[:len(data)-1]) + `,"eventId":"ev-7"}`})
	if err != nil || ev.ID != "ev-7" {
		t.Fatalf("payload eventId: %+v %v", ev, err)
	}
	ev, err = decodeEvent(frame{Type: "EVENT", Headers: map[string]any{"eventType": "user_todo_task_create"}, Data: `{}`})
	if err != nil || !strings.HasPrefix(ev.ID, "user_todo_task_create:") {
		t.Fatalf("fallback id: %+v %v", ev, err)
	}
	if _, err := decodeEvent(frame{Type: "EVENT", Data: `{}`}); err == nil {
		t.Fatal("an event without a key is malformed")
	}
}

func TestReconcileResolvesPendingAndPausedSubscriptions(t *testing.T) {
	f := newFakeDWS(t, func(int, *websocket.Conn) {})
	store := &MemoryStore{}
	c := f.client(t)
	ctx := context.Background()
	at := dws.SubscriptionSpec{EventKey: dws.EventIMAt}
	group := dws.SubscriptionSpec{EventKey: dws.EventIMGroup, ConversationID: "cid-1"}

	// A crash left "at" created on DingTalk's side but only pending in the
	// Store; the configuration no longer wants it.
	orphan, _ := c.Events.Subscribe(ctx, at)
	_ = store.SetSubscriptions(ctx, "agent-a", []dws.Subscription{{EventKey: at.EventKey, Fingerprint: at.Fingerprint(), Spec: &at}})
	l := &Listener{Identity: "agent-a", Store: store, Subscriptions: []dws.SubscriptionSpec{group}}
	var st Status
	if err := l.reconcile(ctx, c, &st); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	_, orphanAlive := f.subs[orphan.ID]
	f.mu.Unlock()
	if orphanAlive {
		t.Fatal("the orphaned subscription must be resolved from its spec and cancelled")
	}

	// The group subscription gets paused on DingTalk's side: it delivers
	// nothing, so reconcile cancels and recreates it (same id, active).
	stored, _ := store.Subscriptions(ctx, "agent-a")
	f.mu.Lock()
	sub := f.subs[stored[0].ID]
	sub.Status = 2
	f.subs[sub.ID] = sub
	f.mu.Unlock()
	if err := l.reconcile(ctx, c, &st); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.subs[sub.ID].Status != 1 || st.Subscriptions != 1 {
		t.Fatalf("paused subscription not revived: %+v", f.subs)
	}
}

func TestMalformedEventsReachHandleBeforeTheAck(t *testing.T) {
	got := make(chan Event, 1)
	acked := make(chan ack, 1)
	f := newFakeDWS(t, func(n int, c *websocket.Conn) {
		if n != 1 {
			time.Sleep(time.Second)
			return
		}
		_ = c.WriteJSON(map[string]any{"type": "EVENT", "headers": map[string]string{"messageId": "bad-1"}, "data": "not json"})
		a, _ := readAck(c)
		acked <- a
		time.Sleep(time.Second)
	})
	l := &Listener{Identity: "agent-a", Store: &MemoryStore{}, Options: fast,
		Client:        func(context.Context) (*dws.Client, error) { return f.client(t), nil },
		Subscriptions: []dws.SubscriptionSpec{{EventKey: dws.EventIMAt}},
		Handle:        func(_ context.Context, ev Event) error { got <- ev; return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = l.Run(ctx) }()
	ev := <-got
	if !ev.Malformed || string(ev.Data) != `"not json"` || ev.ID == "" {
		t.Fatalf("event = %+v", ev)
	}
	if a := <-acked; a.Code != 200 {
		t.Fatalf("ack = %+v", a)
	}
}

func TestControlPlaneRefreshesOnABusinessAuthCode(t *testing.T) {
	f := newFakeDWS(t, func(int, *websocket.Conn) {})
	f.validToken = "uat-2" // the client's "uat" has expired
	c, err := dws.NewWithToken(context.Background(), dws.Config{AuthURL: f.srv.URL, GatewayURL: f.srv.URL, SkipVerify: true},
		dws.Token{AccessToken: "uat", RefreshToken: "rt-1", ClientID: "client-1", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Events.Ticket(context.Background()); err != nil {
		t.Fatalf("an auth code inside HTTP 200 must refresh and retry: %v", err)
	}
}

func TestAlertStateSurvivesARestart(t *testing.T) {
	connected := make(chan struct{}, 1)
	f := newFakeDWS(t, func(int, *websocket.Conn) { connected <- struct{}{}; time.Sleep(time.Second) })
	store := &MemoryStore{}
	down := time.Now().Add(-10 * time.Minute)
	_ = store.SetStatus(context.Background(), Status{Identity: "agent-a", State: StateReconnecting, DownSince: down, Alerting: true})
	var mu sync.Mutex
	var alerts []Alert
	opts := fast
	opts.Alert = func(_ context.Context, a Alert) { mu.Lock(); alerts = append(alerts, a); mu.Unlock() }
	l := &Listener{Identity: "agent-a", Store: store, Options: opts,
		Client:        func(context.Context) (*dws.Client, error) { return f.client(t), nil },
		Subscriptions: []dws.SubscriptionSpec{{EventKey: dws.EventIMAt}},
		Handle:        func(context.Context, Event) error { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = l.Run(ctx) }()
	<-connected
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(alerts) != 1 || alerts[0].Kind != "recovered" || !alerts[0].Since.Equal(down) {
		t.Fatalf("alerts = %+v; the restarted listener owes a recovery", alerts)
	}
}

func TestWatchdogStopsAListenerWhoseLeaseCannotBeRenewed(t *testing.T) {
	closed := make(chan struct{})
	f := newFakeDWS(t, func(_ int, c *websocket.Conn) {
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				close(closed)
				return
			}
		}
	})
	var calls atomic.Int32
	block := make(chan struct{})
	defer close(block)
	m := &Manager{Store: &MemoryStore{}, Tick: 20 * time.Millisecond, LeaseTTL: 150 * time.Millisecond, Options: fast,
		Specs: func(ctx context.Context) ([]Spec, error) {
			if calls.Add(1) > 1 {
				<-block // the configuration source hangs: no renewals happen
			}
			return []Spec{{Identity: "agent-a", Subscriptions: []dws.SubscriptionSpec{{EventKey: dws.EventIMAt}}}}, nil
		},
		Client: func(context.Context, string) (*dws.Client, error) { return f.client(t), nil },
		Handle: func(context.Context, string, Event) error { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = m.Run(ctx) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the listener outlived its lease")
	}
}

func TestDialErrorsNeverCarryTheTicket(t *testing.T) {
	f := newFakeDWS(t, func(int, *websocket.Conn) {})
	f.endpoint = "ws://127.0.0.1:99999/connect" // an invalid port: the dial error quotes the URL
	store := &MemoryStore{}
	l := &Listener{Identity: "agent-a", Store: store, Options: fast,
		Client:        func(context.Context) (*dws.Client, error) { return f.client(t), nil },
		Subscriptions: []dws.SubscriptionSpec{{EventKey: dws.EventIMAt}},
		Handle:        func(context.Context, Event) error { return nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_ = l.Run(ctx)
	st, _, _ := store.Status(context.Background(), "agent-a")
	if st.Failures == 0 || strings.Contains(st.LastError, "secret-ticket") {
		t.Fatalf("status = %+v", st)
	}
}

func TestMemoryStoreSweepsExpiredEventIDs(t *testing.T) {
	now := time.Now()
	m := &MemoryStore{Now: func() time.Time { return now }}
	for i := 0; i < 1023; i++ {
		_ = m.MarkHandled(context.Background(), "a", fmt.Sprint(i), time.Second)
	}
	now = now.Add(time.Minute)
	_ = m.MarkHandled(context.Background(), "a", "last", time.Second)
	if n := len(m.seen); n != 1 {
		t.Fatalf("seen = %d, want only the live id", n)
	}
}
