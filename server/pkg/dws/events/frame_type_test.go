package events

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/multica-ai/multica/server/pkg/dws"
)

// Any frame that is not a system frame carries an event and is delivered
// and acknowledged, whatever its type label; a lowercase system frame is
// still a system frame.
func TestListenerDeliversEveryNonSystemFrame(t *testing.T) {
	done := make(chan struct{})
	f := newFakeDWS(t, func(n int, c *websocket.Conn) {
		if n != 1 {
			time.Sleep(time.Second)
			return
		}
		_ = c.WriteJSON(map[string]any{"type": "system", "headers": map[string]string{"messageId": "p1", "topic": "ping"}, "data": `{"x":1}`})
		if a, err := readAck(c); err != nil || a.Data != `{"x":1}` {
			t.Errorf("lowercase system ping = %+v %v", a, err)
		}
		for i, label := range []string{"event", "DATA", "CALLBACK"} {
			id := []string{"e1", "e2", "e3"}[i]
			inner, _ := json.Marshal(map[string]any{"eventId": id, "eventKey": dws.EventIMAllSingleChats, "subId": "sub-1",
				"payload": map[string]any{"corpid": "ding1", "body": map[string]any{"openConversationId": "cid", "content": "hi"}}})
			doubled, _ := json.Marshal(string(inner))
			_ = c.WriteJSON(map[string]any{"type": label,
				"headers": map[string]string{"messageId": "frame-" + id, "eventId": id}, "data": string(doubled)})
			if a, err := readAck(c); err != nil || a.Code != 200 || a.Headers["messageId"] != "frame-"+id {
				t.Errorf("%s frame ack = %+v %v", label, a, err)
			}
		}
		close(done)
		time.Sleep(time.Second)
	})
	var mu sync.Mutex
	var got []string
	l := &Listener{Identity: "agent-a", Store: &MemoryStore{}, Options: fast,
		Client:        func(context.Context) (*dws.Client, error) { return f.client(t), nil },
		Subscriptions: []dws.SubscriptionSpec{{EventKey: dws.EventIMAllSingleChats}},
		Handle: func(_ context.Context, ev Event) error {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, ev.ID)
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
	if len(got) != 3 {
		t.Fatalf("delivered = %v, want e1 e2 e3", got)
	}
}
