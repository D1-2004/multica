package remotemcp

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

// A server built on the MCP SDK's session-map pattern answers an unknown or
// expired Mcp-Session-Id with HTTP 400. That proves the request did not run,
// so the cached session is dropped and re-established once, tools/call
// included.
func TestSessionClientReestablishesSessionAfter400ForCachedSession(t *testing.T) {
	server := newSessionServer(t, true, false)
	server.unknownSessionStatus = http.StatusBadRequest
	cache := NewSessionCache(10*time.Minute, 16)
	mcp, err := server.client(t).MCP(server.URL+"/mcp", cache)
	if err != nil {
		t.Fatal(err)
	}
	call := func() error {
		_, err := mcp.Call(context.Background(), "k", bearer("good"), "tools/call", map[string]any{"name": "search", "arguments": map[string]any{}})
		return err
	}
	if err := call(); err != nil {
		t.Fatal(err)
	}
	if server.initialized.Load() != 1 || server.calls.Load() != 1 {
		t.Fatalf("first call: initialize=%d calls=%d", server.initialized.Load(), server.calls.Load())
	}
	// The upstream restarted and lost its sessions.
	server.expireSessions()
	if err := call(); err != nil {
		t.Fatalf("call after the server dropped its sessions: %v", err)
	}
	if server.initialized.Load() != 2 || server.calls.Load() != 2 || cache.Len() != 1 {
		t.Fatalf("after 400: initialize=%d calls=%d cache=%d", server.initialized.Load(), server.calls.Load(), cache.Len())
	}
}

// A tools/call that ran and then failed with a JSON-RPC error mentioning a
// session is never replayed; tools/list still treats such an error as a
// request for a session.
func TestSessionClientNeverReplaysToolCallOnSessionWordedRPCError(t *testing.T) {
	server := newSessionServer(t, false, false)
	server.rpcError = "Failed to create checkout session"
	mcp, err := server.client(t).MCP(server.URL+"/mcp", NewSessionCache(time.Minute, 4))
	if err != nil {
		t.Fatal(err)
	}
	_, err = mcp.Call(context.Background(), "k", bearer("good"), "tools/call", map[string]any{"name": "create_checkout"})
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("err = %v", err)
	}
	if server.calls.Load() != 1 || server.initialized.Load() != 0 {
		t.Fatalf("tools/call replayed: calls=%d initialize=%d", server.calls.Load(), server.initialized.Load())
	}

	server.mu.Lock()
	server.rpcError = "Session not initialized"
	server.mu.Unlock()
	_, err = mcp.Call(context.Background(), "k2", bearer("good"), "tools/list", map[string]any{})
	if !errors.As(err, &rpcErr) {
		t.Fatalf("tools/list err = %v", err)
	}
	if server.initialized.Load() != 1 || server.calls.Load() != 3 {
		t.Fatalf("tools/list session retry: initialize=%d calls=%d", server.initialized.Load(), server.calls.Load())
	}
}

// Every MCPClient built for the same cached session sends distinct request
// ids: the relay builds one client per call, and session servers route
// responses by id.
func TestSessionClientRequestIDsAreUniqueWithinASharedSession(t *testing.T) {
	server := newSessionServer(t, true, true)
	cache := NewSessionCache(10*time.Minute, 16)
	client := server.client(t)
	// Establish the shared session first.
	first, err := client.MCP(server.URL+"/mcp", cache)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Call(context.Background(), "shared", bearer("good"), "tools/call", map[string]any{"name": "search", "arguments": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mcp, err := client.MCP(server.URL+"/mcp", cache)
			if err == nil {
				_, err = mcp.Call(context.Background(), "shared", bearer("good"), "tools/call", map[string]any{"name": "search", "arguments": map[string]any{}})
			}
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if server.initialized.Load() != 1 {
		t.Fatalf("session was re-established %d times", server.initialized.Load())
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	ids := server.sessionIDs["session-1"]
	if len(ids) != len(errs)+1 {
		t.Fatalf("shared session carried %d requests, want %d", len(ids), len(errs)+1)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("shared session reused request id %s: %v", id, ids)
		}
		seen[id] = true
	}
}
