package runnerws

import (
	"testing"

	"github.com/multica-ai/multica/server/pkg/runnerprotocol"
)

func TestSendRejectsClosedClient(t *testing.T) {
	done := make(chan struct{})
	close(done)
	hub := NewHub()
	hub.byMachine["machine-1"] = &client{
		send: make(chan outboundFrame, 1),
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
	queued := make(chan outboundFrame, 1)
	hub.byMachine["machine-1"] = &client{
		send: queued,
		done: make(chan struct{}),
	}
	frame := []byte("original")
	if !hub.Send("machine-1", frame) {
		t.Fatal("connected Runner client rejected a call")
	}
	frame[0] = 'X'
	if got := string((<-queued).payload); got != "original" {
		t.Fatalf("queued frame = %q, want an owned copy", got)
	}
}

func TestSendAndCloseMarksFinalFrame(t *testing.T) {
	hub := NewHub()
	queued := make(chan outboundFrame, 1)
	hub.byMachine["machine-1"] = &client{
		send: queued,
		done: make(chan struct{}),
	}
	frame := []byte(`{"type":"runner:shutdown"}`)
	if !hub.SendAndClose("machine-1", frame) {
		t.Fatal("connected Runner client rejected a shutdown")
	}
	got := <-queued
	if string(got.payload) != string(frame) || !got.closeAfter {
		t.Fatalf("shutdown frame = %#v", got)
	}
}

func TestCloseStopsConnectedClient(t *testing.T) {
	hub := NewHub()
	done := make(chan struct{})
	hub.byMachine["machine-1"] = &client{done: done}
	if !hub.Close("machine-1") {
		t.Fatal("connected Runner client was not closed")
	}
	select {
	case <-done:
	default:
		t.Fatal("Runner client done channel is still open")
	}
}

func TestMCPInventoryIsMachineScopedAndDefensivelyCopied(t *testing.T) {
	hub := NewHub()
	inventory := runnerprotocol.MCPInventory{
		Type: runnerprotocol.MessageInventory, Revision: "sha256:revision",
		Servers: []runnerprotocol.MCPServerSummary{{
			Name: "wiki", Transport: "stdio", Availability: "available", Fingerprint: "sha256:fingerprint",
			Tools: []runnerprotocol.MCPToolSummary{{Name: "search", Description: "Search the wiki"}},
		}},
		Config:  []byte(`{"mcpServers":{"wiki":{"command":"node","env":{"TOKEN":"secret"}}}}`),
	}
	if !hub.storeMCPInventory("machine-1", inventory) {
		t.Fatal("valid MCP inventory rejected")
	}
	inventory.Servers[0].Name = "mutated"
	inventory.Servers[0].Tools[0].Name = "mutated-tool"
	inventory.Config[0] = '['
	got, ok := hub.MCPInventory("machine-1")
	if !ok || got.Servers[0].Name != "wiki" || got.Servers[0].Tools[0].Name != "search" || got.Config[0] != '{' {
		t.Fatalf("stored MCP inventory = %#v, ok=%v", got, ok)
	}
	got.Servers[0].Name = "mutated-again"
	got.Servers[0].Tools[0].Name = "mutated-tool-again"
	got.Config[0] = '['
	again, _ := hub.MCPInventory("machine-1")
	if again.Servers[0].Name != "wiki" || again.Servers[0].Tools[0].Name != "search" || again.Config[0] != '{' {
		t.Fatal("returned MCP inventory aliases Hub state")
	}
	if _, ok := hub.MCPInventory("machine-2"); ok {
		t.Fatal("inventory leaked across machines")
	}
}
