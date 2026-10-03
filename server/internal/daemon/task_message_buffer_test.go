package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/multica-ai/multica/server/pkg/agent"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestTaskMessageBufferArrivalOrderAndLostAck(t *testing.T) {
	b := taskMessageBuffer{seq: new(atomic.Int32)}
	b.append(TaskMessageData{Type: "text", Content: "before "})
	b.append(TaskMessageData{Type: "text", Content: "tool"})
	b.append(TaskMessageData{Type: "tool_use", Tool: "bash"})
	b.append(TaskMessageData{Type: "text", Content: "after"})
	first := b.take()
	if len(first) != 3 || first[0].Content != "before tool" || first[1].Type != "tool_use" || first[2].Content != "after" {
		t.Fatal(first)
	}
	for i, m := range first {
		if m.Seq != i+1 {
			t.Fatal(first)
		}
	}
	b.retry(first)
	// The server may have committed first before the response was lost. Adding
	// text must not mutate its content and cause a conflicting retry.
	b.append(TaskMessageData{Type: "text", Content: " later"})
	retry := b.take()
	if len(retry) != 4 || retry[2].Content != "after" || retry[3].Seq != 4 {
		t.Fatal(retry)
	}
}

func TestExecuteAndDrainRetriesFailedTranscriptBatch(t *testing.T) {
	var mu sync.Mutex
	var attempts [][]TaskMessageData
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/messages") {
			var body struct {
				Messages []TaskMessageData `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			mu.Lock()
			attempts = append(attempts, body.Messages)
			n := len(attempts)
			mu.Unlock()
			if n == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	d := &Daemon{client: NewClient(srv.URL), logger: slog.Default()}
	_, _, err := d.executeAndDrain(context.Background(), &transcriptBackend{}, "prompt", agent.ExecOptions{}, slog.Default(), "retry-events", "", new(atomic.Int32))
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 2 || !reflect.DeepEqual(attempts[0], attempts[1]) {
		t.Fatalf("retry changed the immutable prefix: %+v", attempts)
	}
}

type queuedTaskEventBackend struct{ transcriptBackend }

func (b *queuedTaskEventBackend) Execute(_ context.Context, _ string, _ agent.ExecOptions) (*agent.Session, error) {
	messages := make(chan agent.Message, 50)
	for i := range 50 {
		messages <- agent.Message{Type: agent.MessageText, Content: "tail", MessageID: fmt.Sprint(i)}
	}
	close(messages)
	result := make(chan agent.Result, 1)
	result <- agent.Result{Status: "cancelled"}
	close(result)
	return &agent.Session{Messages: messages, Result: result}, nil
}

func TestExecuteAndDrainCancelledContextDrainsQueuedTail(t *testing.T) {
	d, rec := newTranscriptRecorder(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := d.executeAndDrain(ctx, &queuedTaskEventBackend{}, "prompt", agent.ExecOptions{}, slog.Default(), "cancel-tail", "", new(atomic.Int32))
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.snapshot(); len(got) != 50 {
		t.Fatalf("queued tail lost: %d of 50", len(got))
	}
}

func TestTaskMessageBufferDoesNotCombineMessagesOrPhases(t *testing.T) {
	b := taskMessageBuffer{seq: new(atomic.Int32)}
	for _, source := range []protocol.TaskEventSource{{MessageID: "one", Phase: "delta"}, {MessageID: "two", Phase: "delta"}, {MessageID: "two", Phase: "final"}} {
		s := source
		b.append(TaskMessageData{Type: "text", Content: "part", Event: &s})
	}
	if got := b.take(); len(got) != 3 {
		t.Fatal(got)
	}
}
