package daemon

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

type delayedStopBackend struct {
	started chan struct{}
	stopped chan struct{}
}

func (b delayedStopBackend) Name() string { return "delayed-stop" }
func (b delayedStopBackend) Execute(ctx context.Context, _ string, _ agent.ExecOptions) (*agent.Session, error) {
	messages := make(chan agent.Message)
	result := make(chan agent.Result, 1)
	close(b.started)
	go func() {
		<-ctx.Done()
		// Model a process group that outlives cancellation and the leader.
		time.Sleep(100 * time.Millisecond)
		close(b.stopped)
		close(messages)
		result <- agent.Result{Status: "aborted", ProcessGroupStopped: true, SessionID: "original-session"}
		close(result)
	}()
	return &agent.Session{Messages: messages, Result: result}, nil
}

func TestSteerCancellationJoinsProviderBeforeAcknowledgement(t *testing.T) {
	d := newTestDaemon(t)
	b := delayedStopBackend{started: make(chan struct{}), stopped: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan agent.Result, 1)
	go func() {
		r, _, err := d.executeAndDrain(ctx, b, "prompt", agent.ExecOptions{}, slog.Default(), "steer-task", "", new(atomic.Int32))
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	<-b.started
	cancel()
	select {
	case <-done:
		t.Fatal("returned before the old process group exited")
	case <-time.After(40 * time.Millisecond):
	}
	select {
	case r := <-done:
		select {
		case <-b.stopped:
		default:
			t.Fatal("missing process exit")
		}
		if !r.ProcessGroupStopped || r.SessionID != "original-session" || r.Status != "cancelled" {
			t.Fatalf("cancel result lost exit proof/session: %+v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("did not return after provider cleanup")
	}
}
