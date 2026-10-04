package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	openai "github.com/openai/openai-go/v3"
)

func TestChatStreamedRejectsEOFAndClosesOnObserverCancellation(t *testing.T) {
	for _, mode := range []string{"unfinished", "cancelled", "observer_error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			closed := make(chan struct{})
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"}}]}\n\n")
				w.(http.Flusher).Flush()
				if mode != "unfinished" {
					<-r.Context().Done()
				}
				close(closed)
			}))
			defer srv.Close()
			observerErr := errors.New("observer failed")
			observe := func(openai.ChatCompletionChunk) error {
				if mode == "cancelled" {
					cancel()
				}
				if mode == "observer_error" {
					return observerErr
				}
				return nil
			}
			c := New(Config{APIKey: "fixture", BaseURL: srv.URL, MaxRetries: -1})
			out, err := c.ChatStreamed(ctx, openai.ChatCompletionNewParams{Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")}}, observe)
			if err == nil || out == nil || requests.Load() != 1 {
				t.Fatal("unfinished/cancelled stream accepted or retried", out, err, requests.Load())
			}
			if mode == "cancelled" && !errors.Is(err, context.Canceled) || mode == "observer_error" && !errors.Is(err, observerErr) {
				t.Fatal("observer/cancellation cause lost", err)
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("provider connection remained open")
			}
		})
	}
}
