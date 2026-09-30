package dingtalkresponse

import (
	"context"
	"os"
	"path/filepath"
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

// A consumer running on the dws CLI when the switch goes on ends, so the
// decision service reopens the identity on the shared connection instead
// of holding a second stream until the consumer's cycle ends.
func TestCLIConsumerEndsWhenSharedConnectionsSwitchOn(t *testing.T) {
	previous := switchCheckInterval
	switchCheckInterval = 20 * time.Millisecond
	defer func() { switchCheckInterval = previous }()
	dir := t.TempDir()
	path := filepath.Join(dir, "dws")
	script := "#!/bin/sh\nprintf '[event] ready subscription\\n' >&2\nexec sleep 30\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	events := &fakeEventConnections{}
	session := &decisionSession{cli: dwsclient.CLI{Path: path}, dir: dir, events: events,
		identity: dwsclient.Identity{AgentID: "a", UID: "1", OrgID: "o"}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	readied := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- session.Consume(ctx, func() { close(readied) }, func([]byte) error { return nil })
	}()
	select {
	case <-readied:
	case <-time.After(5 * time.Second):
		t.Fatal("the CLI consumer never reported ready")
	}
	// Still off: the CLI consumer keeps running.
	select {
	case err := <-done:
		t.Fatalf("the CLI consumer ended while the switch was off: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	events.active.Store(true)
	start := time.Now()
	select {
	case err := <-done:
		if err == nil || err.Error() != "DWS event connections switched on" {
			t.Fatalf("err = %v", err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("took %s to hand over", elapsed)
		}
		if events.kicks.Load() != 1 {
			t.Fatalf("kicks = %d; the handover must start the shared stream at once", events.kicks.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the CLI consumer kept running after the switch went on")
	}
}
