package daemonws

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func newNotifierTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Skipf("database not available: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("database not available: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// registerTestClient registers a client on the hub for runtimeID and returns
// its send channel — the buffer a real daemon's socket writer drains. No
// websocket needed: notifyFrame's delivery target is exactly this channel.
func registerTestClient(t *testing.T, h *Hub, runtimeID string) <-chan []byte {
	t.Helper()
	c := &client{
		hub:      h,
		send:     make(chan []byte, 8),
		runtimes: map[string]struct{}{runtimeID: {}},
		seenIDs:  map[string]struct{}{},
	}
	h.mu.Lock()
	h.clients[c] = true
	if h.byRuntime[runtimeID] == nil {
		h.byRuntime[runtimeID] = map[*client]bool{}
	}
	h.byRuntime[runtimeID][c] = true
	h.mu.Unlock()
	t.Cleanup(func() {
		h.mu.Lock()
		delete(h.clients, c)
		delete(h.byRuntime, runtimeID)
		h.mu.Unlock()
	})
	return c.send
}

// waitForListener blocks until the notifier's LISTEN is live. NOTIFY is not
// durable: a notification published before the listener subscribes is simply
// gone, which would make these tests flaky rather than wrong.
func waitForListener(t *testing.T, n *PGNotifier) {
	t.Helper()
	probe := NewHub()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		// Publishing from a throwaway node proves the listener is attached:
		// the notifier under test delivers it to its own hub.
		other := NewPGNotifier(probe, n.pool, slog.Default())
		const probeRuntime = "00000000-0000-0000-0000-0000000000ff"
		frames := make(chan []byte, 1)
		n.local.mu.Lock()
		c := &client{hub: n.local, send: frames, runtimes: map[string]struct{}{probeRuntime: {}}, seenIDs: map[string]struct{}{}}
		n.local.clients[c] = true
		n.local.byRuntime[probeRuntime] = map[*client]bool{c: true}
		n.local.mu.Unlock()

		other.NotifyTaskAvailable(probeRuntime, "probe")

		select {
		case <-frames:
			n.local.mu.Lock()
			delete(n.local.clients, c)
			delete(n.local.byRuntime, probeRuntime)
			n.local.mu.Unlock()
			return
		case <-time.After(200 * time.Millisecond):
		}
		n.local.mu.Lock()
		delete(n.local.clients, c)
		delete(n.local.byRuntime, probeRuntime)
		n.local.mu.Unlock()
	}
	t.Fatal("listener did not attach within timeout")
}

// waitForFrame reads the next frame the hub pushed to a runtime's client, or
// fails. The client is a real hub registration, so this exercises the whole
// path: NOTIFY → listener → local hub → the daemon's socket buffer.
func waitForFrame(t *testing.T, ch <-chan []byte, within time.Duration) protocol.Message {
	t.Helper()
	select {
	case raw := <-ch:
		var msg protocol.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		return msg
	case <-time.After(within):
		t.Fatal("no wakeup frame delivered within timeout")
		return protocol.Message{}
	}
}

// The regression: the daemon's wakeup socket lives on one replica while the
// task is enqueued on another. The in-process hub only knows its own sockets,
// so the wakeup was dropped and the daemon waited out a 30s poll interval.
func TestPGNotifierDeliversWakeupAcrossNodes(t *testing.T) {
	pool := newNotifierTestPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Node B holds the daemon's socket.
	hubB := NewHub()
	nodeB := NewPGNotifier(hubB, pool, slog.Default())
	go nodeB.Listen(ctx)

	// Node A serves the HTTP request that enqueues the task. Its own hub has
	// no clients — exactly the production case that was losing wakeups.
	hubA := NewHub()
	nodeA := NewPGNotifier(hubA, pool, slog.Default())

	const runtimeID = "11111111-1111-1111-1111-111111111111"
	frames := registerTestClient(t, hubB, runtimeID)

	// Give node B's listener time to issue its LISTEN before we publish;
	// NOTIFY is not durable, so a notification sent before LISTEN is lost.
	waitForListener(t, nodeB)

	nodeA.NotifyTaskAvailable(runtimeID, "22222222-2222-2222-2222-222222222222")

	msg := waitForFrame(t, frames, 5*time.Second)
	if msg.Type != protocol.EventDaemonTaskAvailable {
		t.Fatalf("frame type = %q, want %q", msg.Type, protocol.EventDaemonTaskAvailable)
	}
	var payload protocol.TaskAvailablePayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.RuntimeID != runtimeID {
		t.Errorf("runtime id = %q, want %q", payload.RuntimeID, runtimeID)
	}
	if payload.TaskID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("task id = %q", payload.TaskID)
	}
}

// A node that publishes must not also re-deliver its own notification: it
// already delivered locally before publishing, and the daemon's dedupe would
// otherwise be doing work the origin check can do for free.
func TestPGNotifierDeliversLocallyExactlyOnce(t *testing.T) {
	pool := newNotifierTestPool(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hub := NewHub()
	node := NewPGNotifier(hub, pool, slog.Default())
	go node.Listen(ctx)

	const runtimeID = "33333333-3333-3333-3333-333333333333"
	frames := registerTestClient(t, hub, runtimeID)
	waitForListener(t, node)

	node.NotifyTaskAvailable(runtimeID, "44444444-4444-4444-4444-444444444444")

	waitForFrame(t, frames, 5*time.Second) // the local delivery
	select {
	case <-frames:
		t.Fatal("origin node re-delivered its own notification")
	case <-time.After(500 * time.Millisecond):
	}
}
