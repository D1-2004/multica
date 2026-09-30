package events

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/multica-ai/multica/server/pkg/dws"
)

// keepaliveListener runs a listener with a short ReadIdle against f and
// returns what it handled.
func keepaliveListener(t *testing.T, f *fakeDWS, opts Options) (*[]Event, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var got []Event
	l := &Listener{Identity: "agent-a", Store: &MemoryStore{}, Options: opts,
		Client:        func(context.Context) (*dws.Client, error) { return f.client(t), nil },
		Subscriptions: []dws.SubscriptionSpec{{EventKey: dws.EventIMAt}},
		Handle: func(_ context.Context, ev Event) error {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, ev)
			return nil
		}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = l.Run(ctx) }()
	return &got, &mu
}

// readUntilClosed reads c in the background; reading is what makes gorilla
// run c's control handlers.
func readUntilClosed(c *websocket.Conn) {
	go func() {
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}()
}

// A server that sends nothing for several ReadIdle periods but answers
// WebSocket pings keeps the one connection: the pong is proof of life.
func TestListenerPingsAQuietServerInsteadOfReconnecting(t *testing.T) {
	var pings atomic.Int32
	done := make(chan struct{})
	f := newFakeDWS(t, func(n int, c *websocket.Conn) {
		if n != 1 {
			time.Sleep(time.Second)
			return
		}
		c.SetPingHandler(func(data string) error {
			pings.Add(1)
			return c.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(time.Second))
		})
		readUntilClosed(c)
		// Silent for 4x ReadIdle, then one event.
		time.Sleep(600 * time.Millisecond)
		sendEvent(t, c, "e1", dws.EventIMAt, map[string]any{"openConversationId": "cid"})
		time.Sleep(100 * time.Millisecond)
		close(done)
		time.Sleep(time.Second)
	})
	opts := fast
	opts.ReadIdle, opts.PingInterval = 150*time.Millisecond, 40*time.Millisecond
	got, mu := keepaliveListener(t, f, opts)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
	mu.Lock()
	defer mu.Unlock()
	if f.conns.Load() != 1 || len(*got) != 1 || pings.Load() < 5 {
		t.Fatalf("connections = %d, handled = %d, pings = %d; want one connection kept by its pings",
			f.conns.Load(), len(*got), pings.Load())
	}
}

// Server pings extend the deadline too, and the listener answers them.
func TestListenerAnswersServerPings(t *testing.T) {
	var pongs atomic.Int32
	done := make(chan struct{})
	f := newFakeDWS(t, func(n int, c *websocket.Conn) {
		if n != 1 {
			time.Sleep(time.Second)
			return
		}
		c.SetPongHandler(func(string) error { pongs.Add(1); return nil })
		readUntilClosed(c)
		for i := 0; i < 15; i++ {
			_ = c.WriteControl(websocket.PingMessage, []byte("hi"), time.Now().Add(time.Second))
			time.Sleep(40 * time.Millisecond)
		}
		close(done)
		time.Sleep(time.Second)
	})
	opts := fast
	opts.ReadIdle, opts.PingInterval = 150*time.Millisecond, time.Hour
	keepaliveListener(t, f, opts)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
	if f.conns.Load() != 1 || pongs.Load() < 10 {
		t.Fatalf("connections = %d, pongs = %d", f.conns.Load(), pongs.Load())
	}
}

// A server that swallows pings is still dropped after ReadIdle.
func TestListenerDropsAServerThatNeverAnswers(t *testing.T) {
	f := newFakeDWS(t, func(n int, c *websocket.Conn) {
		c.SetPingHandler(func(string) error { return nil })
		readUntilClosed(c)
		time.Sleep(2 * time.Second)
	})
	opts := fast
	opts.ReadIdle, opts.PingInterval = 150*time.Millisecond, 40*time.Millisecond
	keepaliveListener(t, f, opts)
	waitUntil(t, "a reconnect", func() bool { return f.conns.Load() >= 2 })
}
