package runnerws

import "testing"

func TestSendRejectsClosedClient(t *testing.T) {
	done := make(chan struct{})
	close(done)
	hub := NewHub()
	hub.byMachine["machine-1"] = &client{
		send: make(chan []byte, 1),
		done: done,
	}
	if hub.Send("machine-1", []byte(`{"type":"runner:call"}`)) {
		t.Fatal("closed Runner client accepted a call")
	}
	if hub.Connected("machine-1") {
		t.Fatal("closed Runner client reported connected")
	}
}

func TestSendCopiesFrameBeforeQueueing(t *testing.T) {
	hub := NewHub()
	queued := make(chan []byte, 1)
	hub.byMachine["machine-1"] = &client{
		send: queued,
		done: make(chan struct{}),
	}
	frame := []byte("original")
	if !hub.Send("machine-1", frame) {
		t.Fatal("connected Runner client rejected a call")
	}
	frame[0] = 'X'
	if got := string(<-queued); got != "original" {
		t.Fatalf("queued frame = %q, want an owned copy", got)
	}
}
