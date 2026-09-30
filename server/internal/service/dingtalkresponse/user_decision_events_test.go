package dingtalkresponse

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/dwsclient"
)

type fakeEventConnections struct {
	active, ready atomic.Bool
	kicks         atomic.Int32
}

func (f *fakeEventConnections) Active() bool                                   { return f.active.Load() }
func (f *fakeEventConnections) Ready(context.Context, dwsclient.Identity) bool { return f.ready.Load() }
func (f *fakeEventConnections) Kick()                                          { f.kicks.Add(1) }

// A decision session waits for its identity's shared stream: ready once it
// is connected, and it ends as soon as the switch goes off, so the decision
// service reopens the identity on the dws CLI.
func TestAwaitEventConnection(t *testing.T) {
	events := &fakeEventConnections{}
	events.active.Store(true)
	events.ready.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readied := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- awaitEventConnection(ctx, events, dwsclient.Identity{AgentID: "a", UID: "1", OrgID: "o"}, func() { close(readied) })
	}()
	select {
	case <-readied:
	case <-time.After(3 * time.Second):
		t.Fatal("never reported ready")
	}
	if events.kicks.Load() != 1 {
		t.Fatalf("kicks = %d", events.kicks.Load())
	}
	events.active.Store(false)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("switch-off must end the consumer with an error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the consumer kept waiting after the switch went off")
	}
	// A cancelled context ends it too.
	events.active.Store(true)
	cctx, ccancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); ccancel() }()
	if err := awaitEventConnection(cctx, events, dwsclient.Identity{AgentID: "a", UID: "1", OrgID: "o"}, func() {}); err == nil {
		t.Fatal("a cancelled wait must return its context error")
	}
}
