package events

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/multica-ai/multica/server/pkg/dws"
)

// After the host failed an event, only a handled event ends the outage: not
// a later failure of another kind, a stable connection, or a restart. The
// outage alerts while such a connection stays open.
func TestListenerHandleFailureNeedsAHandledEventToRecover(t *testing.T) {
	f := newFakeDWS(t, func(n int, c *websocket.Conn) {
		if n == 1 {
			sendEvent(t, c, "e1", dws.EventIMAt, map[string]any{"openConversationId": "cid"})
			_, _ = readAck(c)
			return
		}
		time.Sleep(2 * time.Second) // connected, nothing to handle
	})
	var log alertLog
	opts := fast
	opts.AlertAfter = 150 * time.Millisecond
	opts.Alert = log.add
	store := &MemoryStore{}
	var failed atomic.Bool
	listener := func() *Listener {
		return &Listener{Identity: "agent-a", Store: store, Options: opts,
			Client:        func(context.Context) (*dws.Client, error) { return f.client(t), nil },
			Subscriptions: []dws.SubscriptionSpec{{EventKey: dws.EventIMAt}},
			Handle: func(context.Context, Event) error {
				if failed.CompareAndSwap(false, true) {
					// The next ticket fails too: an error that is not the host's.
					f.mu.Lock()
					f.ticketFails = 1
					f.mu.Unlock()
				}
				return errors.New("database down")
			}}
	}
	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan struct{})
	go func() { _ = listener().Run(ctx1); close(done1) }()
	waitUntil(t, "a down alert while connected", func() bool { return log.has("down") })
	st, _, _ := store.Status(context.Background(), "agent-a")
	if st.State != StateConnected || !st.HandleFailing {
		t.Fatalf("status = %+v", st)
	}
	cancel1()
	<-done1
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _ = listener().Run(ctx2) }()
	time.Sleep(300 * time.Millisecond)
	if log.has("recovered") {
		t.Fatalf("alerts = %v; nothing was handled since the failure", log.kinds())
	}
}

// failingStatusStore fails the first SetStatus of one identity.
type failingStatusStore struct {
	*MemoryStore
	identity string
	fails    atomic.Int32
}

func (s *failingStatusStore) SetStatus(ctx context.Context, st Status) error {
	if st.Identity == s.identity && s.fails.Add(-1) >= 0 {
		return errors.New("store down")
	}
	return s.MemoryStore.SetStatus(ctx, st)
}

// An identity that never got a subscription recorded but is alerting is
// still retired when dropped, and a failed status write is retried.
func TestManagerRetiresAnAlertingIdentityWithoutSubscriptions(t *testing.T) {
	mem := &MemoryStore{}
	_ = mem.SetStatus(context.Background(), Status{Identity: "gone", State: StateReconnecting, Alerting: true, DownSince: time.Now()})
	store := &failingStatusStore{MemoryStore: mem, identity: "gone"}
	store.fails.Store(1)
	var log alertLog
	opts := fast
	opts.Alert = log.add
	m := &Manager{Store: store, Tick: 20 * time.Millisecond, LeaseTTL: 150 * time.Millisecond, Options: opts,
		Specs:  func(context.Context) ([]Spec, error) { return nil, nil },
		Client: func(context.Context, string) (*dws.Client, error) { return nil, errors.New("not needed") },
		Handle: func(context.Context, string, Event) error { return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = m.Run(ctx) }()
	waitUntil(t, "status stopped", func() bool {
		st, _, _ := mem.Status(context.Background(), "gone")
		return st.State == StateStopped && !st.Alerting
	})
	if !log.has("retired") {
		t.Fatalf("alerts = %v", log.kinds())
	}
}
