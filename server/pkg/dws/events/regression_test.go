package events

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/multica-ai/multica/server/pkg/dws"
)

type alertLog struct {
	mu     sync.Mutex
	alerts []Alert
}

func (a *alertLog) add(_ context.Context, al Alert) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.alerts = append(a.alerts, al)
}

func (a *alertLog) kinds() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, al := range a.alerts {
		out = append(out, al.Kind)
	}
	return out
}

func (a *alertLog) has(kind string) bool {
	for _, k := range a.kinds() {
		if k == kind {
			return true
		}
	}
	return false
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A host that cannot persist events keeps every connection short: each one
// connects, receives the redelivery and fails it. That is an outage.
func TestListenerAlertsWhileTheHostKeepsFailing(t *testing.T) {
	f := newFakeDWS(t, func(_ int, c *websocket.Conn) {
		sendEvent(t, c, "e1", dws.EventIMAt, map[string]any{"openConversationId": "cid"})
		_, _ = readAck(c)
	})
	var log alertLog
	opts := fast
	opts.AlertAfter = 50 * time.Millisecond
	opts.Alert = log.add
	var handles atomic.Int32
	l := &Listener{Identity: "agent-a", Store: &MemoryStore{}, Options: opts,
		Client:        func(context.Context) (*dws.Client, error) { return f.client(t), nil },
		Subscriptions: []dws.SubscriptionSpec{{EventKey: dws.EventIMAt}},
		Handle: func(context.Context, Event) error {
			handles.Add(1)
			return errors.New("database down")
		}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = l.Run(ctx) }()
	waitUntil(t, "a down alert", func() bool { return log.has("down") })
	log.mu.Lock()
	defer log.mu.Unlock()
	if !strings.Contains(log.alerts[0].Err, "host failed to handle") {
		t.Fatalf("alert = %+v", log.alerts[0])
	}
}

// A server that drops every connection right after the handshake never
// ends the outage.
func TestListenerAlertsWhenConnectionsDropAtOnce(t *testing.T) {
	f := newFakeDWS(t, func(int, *websocket.Conn) {})
	var log alertLog
	opts := fast
	opts.AlertAfter = 50 * time.Millisecond
	opts.StableAfter = time.Second
	opts.Alert = log.add
	store := &MemoryStore{}
	l := &Listener{Identity: "agent-a", Store: store, Options: opts,
		Client:        func(context.Context) (*dws.Client, error) { return f.client(t), nil },
		Subscriptions: []dws.SubscriptionSpec{{EventKey: dws.EventIMAt}},
		Handle:        func(context.Context, Event) error { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = l.Run(ctx) }()
	waitUntil(t, "a down alert", func() bool { return log.has("down") })
	if st, _, _ := store.Status(context.Background(), "agent-a"); st.Failures < 2 {
		t.Fatalf("failures = %d; consecutive short connections must count", st.Failures)
	}
}

// An outage and its alert survive a graceful stop (restart, configuration
// change, takeover): the next run closes the alert when it recovers.
func TestListenerResumesTheAlertAfterAGracefulStop(t *testing.T) {
	f := newFakeDWS(t, func(int, *websocket.Conn) { time.Sleep(time.Second) })
	f.ticketFails = 1000
	var log alertLog
	opts := fast
	opts.AlertAfter = 30 * time.Millisecond
	opts.Alert = log.add
	store := &MemoryStore{}
	listener := func() *Listener {
		return &Listener{Identity: "agent-a", Store: store, Options: opts,
			Client:        func(context.Context) (*dws.Client, error) { return f.client(t), nil },
			Subscriptions: []dws.SubscriptionSpec{{EventKey: dws.EventIMAt}},
			Handle:        func(context.Context, Event) error { return nil }}
	}
	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan struct{})
	go func() { _ = listener().Run(ctx1); close(done1) }()
	waitUntil(t, "a down alert", func() bool { return log.has("down") })
	cancel1()
	<-done1
	if st, _, _ := store.Status(context.Background(), "agent-a"); st.State != StateStopped || !st.Alerting || st.DownSince.IsZero() {
		t.Fatalf("stopped status = %+v", st)
	}
	f.mu.Lock()
	f.ticketFails = 0
	f.mu.Unlock()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _ = listener().Run(ctx2) }()
	waitUntil(t, "a recovered alert", func() bool { return log.has("recovered") })
	if kinds := log.kinds(); len(kinds) != 2 {
		t.Fatalf("alerts = %v; the restart must not raise a second down", kinds)
	}
}

// A wanted subscription is paused and cancelling it fails once: the record
// and the subscription DWS hands back share one id, which must never be
// cancelled as a duplicate of itself.
func TestReconcileNeverCancelsTheSubscriptionItKeeps(t *testing.T) {
	f := newFakeDWS(t, func(int, *websocket.Conn) {})
	store := &MemoryStore{}
	c := f.client(t)
	ctx := context.Background()
	group := dws.SubscriptionSpec{EventKey: dws.EventIMGroup, ConversationID: "cid-1"}
	l := &Listener{Identity: "agent-a", Store: store, Subscriptions: []dws.SubscriptionSpec{group}}
	var st Status
	if err := l.reconcile(ctx, c, &st); err != nil {
		t.Fatal(err)
	}
	stored, _ := store.Subscriptions(ctx, "agent-a")
	id := stored[0].ID
	f.mu.Lock()
	sub := f.subs[id]
	sub.Status = 2 // paused on DingTalk's side
	f.subs[id] = sub
	f.cancelFails = 1
	f.mu.Unlock()
	if err := l.reconcile(ctx, c, &st); err == nil {
		t.Fatal("a failed cancel was not reported")
	}
	if stored, _ = store.Subscriptions(ctx, "agent-a"); len(stored) != 1 {
		t.Fatalf("stored = %+v; one record per subscription id", stored)
	}
	if err := l.reconcile(ctx, c, &st); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	live, alive := f.subs[id]
	f.mu.Unlock()
	if !alive || live.Status != 1 || st.Subscriptions != 1 {
		t.Fatalf("subscription %s alive=%v %+v, st.Subscriptions=%d", id, alive, live, st.Subscriptions)
	}
}

// A frame that is not JSON cannot be acked; it is recorded once and the
// stream goes on.
func TestListenerRecordsAnUnreadableFrameAndReadsOn(t *testing.T) {
	done := make(chan struct{})
	f := newFakeDWS(t, func(n int, c *websocket.Conn) {
		if n != 1 {
			time.Sleep(time.Second)
			return
		}
		_ = c.WriteMessage(websocket.TextMessage, []byte("not json"))
		sendEvent(t, c, "e1", dws.EventIMAt, map[string]any{"openConversationId": "cid"})
		if a, err := readAck(c); err != nil || a.Headers["messageId"] != "frame-e1" {
			t.Errorf("ack = %+v %v", a, err)
		}
		close(done)
		time.Sleep(time.Second)
	})
	var mu sync.Mutex
	var got []Event
	l := &Listener{Identity: "agent-a", Store: &MemoryStore{}, Options: fast,
		Client:        func(context.Context) (*dws.Client, error) { return f.client(t), nil },
		Subscriptions: []dws.SubscriptionSpec{{EventKey: dws.EventIMAt}},
		Handle: func(_ context.Context, ev Event) error {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, ev)
			return nil
		}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = l.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || !got[0].Malformed || string(got[0].Data) != `"not json"` || got[1].ID != "e1" || f.conns.Load() != 1 {
		t.Fatalf("handled = %+v on %d connections", got, f.conns.Load())
	}
}

// Retiring a dropped identity is slow (its mint hangs): the healthy listener
// of another identity on the same replica keeps its connection.
func TestManagerRetiresOffTheRenewalLoop(t *testing.T) {
	f := newFakeDWS(t, func(_ int, c *websocket.Conn) {
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	})
	store := &MemoryStore{}
	_ = store.SetSubscriptions(context.Background(), "gone", []dws.Subscription{{ID: "sub-x", EventKey: dws.EventIMAt}})
	var mints atomic.Int32
	m := &Manager{Store: store, Tick: 20 * time.Millisecond, LeaseTTL: 150 * time.Millisecond, Options: fast,
		Specs: func(context.Context) ([]Spec, error) {
			return []Spec{{Identity: "agent-a", Subscriptions: []dws.SubscriptionSpec{{EventKey: dws.EventIMAt}}}}, nil
		},
		Client: func(ctx context.Context, id string) (*dws.Client, error) {
			if id == "gone" {
				mints.Add(1)
				time.Sleep(300 * time.Millisecond)
				return nil, errors.New("agent not found")
			}
			return f.client(t), nil
		},
		Handle: func(context.Context, string, Event) error { return nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_ = m.Run(ctx)
	if n := f.conns.Load(); n != 1 {
		t.Fatalf("agent-a connected %d times; a slow retirement restarted it", n)
	}
	// Failing retirements back off instead of retrying every tick.
	if n := mints.Load(); n > 4 {
		t.Fatalf("retirement attempts = %d in 1.5s", n)
	}
}

// Dropping an identity while it alerts closes the alert.
func TestManagerClosesTheAlertOfARetiredIdentity(t *testing.T) {
	f := newFakeDWS(t, func(int, *websocket.Conn) { time.Sleep(time.Second) })
	f.ticketFails = 1000
	store := &MemoryStore{}
	var log alertLog
	opts := fast
	opts.AlertAfter = 30 * time.Millisecond
	opts.Alert = log.add
	var specsMu sync.Mutex
	specs := []Spec{{Identity: "agent-a", Subscriptions: []dws.SubscriptionSpec{{EventKey: dws.EventIMAt}}}}
	m := &Manager{Store: store, Tick: 20 * time.Millisecond, LeaseTTL: 150 * time.Millisecond, Options: opts,
		Specs: func(context.Context) ([]Spec, error) {
			specsMu.Lock()
			defer specsMu.Unlock()
			return append([]Spec(nil), specs...), nil
		},
		Client: func(context.Context, string) (*dws.Client, error) { return f.client(t), nil },
		Handle: func(context.Context, string, Event) error { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = m.Run(ctx) }()
	waitUntil(t, "a down alert", func() bool { return log.has("down") })
	specsMu.Lock()
	specs = nil
	specsMu.Unlock()
	waitUntil(t, "a retired alert", func() bool { return log.has("retired") })
	waitUntil(t, "status stopped", func() bool {
		st, _, _ := store.Status(context.Background(), "agent-a")
		return st.State == StateStopped && !st.Alerting
	})
}
