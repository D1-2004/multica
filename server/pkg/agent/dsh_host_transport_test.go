package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// In-memory pipes exercise the wire boundary without starting a Host or server.
func dshHostPipe(t *testing.T, reply string) (*dshHostClient, <-chan map[string]any) {
	t.Helper()
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = peer.Close() })
	requests := make(chan map[string]any, 1)
	go func() {
		defer peer.Close()
		line, err := bufio.NewReader(peer).ReadBytes('\n')
		if err != nil {
			return
		}
		var request map[string]any
		if json.Unmarshal(line, &request) != nil {
			return
		}
		requests <- request
		_, _ = io.WriteString(peer, reply)
	}()
	return &dshHostClient{
		identity: dshHostIdentity{WorkspaceID: "workspace-fixture", AgentID: "agent-fixture", Generation: 7},
		dial:     func(context.Context) (net.Conn, error) { return client, nil },
	}, requests
}

func TestDSHHostControlPreservesIdentityAndRejectsInvalidResponses(t *testing.T) {
	client, requests := dshHostPipe(t, "{\"ok\":true,\"value\":{\"accepted\":true}}\n")
	value, err := client.call(context.Background(), "prompt", map[string]any{"sessionId": "session-fixture"})
	if err != nil || string(value) != `{"accepted":true}` {
		t.Fatalf("control receipt = %s, %v", value, err)
	}
	request := <-requests
	identity := request["identity"].(map[string]any)
	if identity["generation"] != float64(7) || identity["agent_id"] != "agent-fixture" || request["method"] != "prompt" {
		t.Fatalf("wrong control identity: %v", request)
	}
	for _, reply := range []string{"", `{"ok":true,"value":{}}`, "{\"ok\":false,\"error\":\"fixture-secret\"}\n", "{\"ok\":true,\"value\":null}\n", strings.Repeat("x", dshHostMaxFrame) + "\n"} {
		client, _ := dshHostPipe(t, reply)
		_, err := client.call(context.Background(), "health", nil)
		if err == nil || strings.Contains(err.Error(), "fixture-secret") {
			t.Fatalf("invalid response accepted or diagnostic leaked: %v", err)
		}
	}
}

func TestDSHHostFollowRequiresConsumerCompletionAndClosesOnCancellation(t *testing.T) {
	const frames = "{\"ok\":true,\"value\":{\"type\":\"snapshot\"}}\n{\"ok\":true,\"value\":{\"type\":\"heartbeat\"}}\n"
	client, _ := dshHostPipe(t, frames)
	count := 0
	err := client.follow(context.Background(), nil, func(json.RawMessage) (bool, error) { count++; return false, nil })
	if err == nil || count != 2 {
		t.Fatalf("EOF should fail after two frames: count=%d error=%v", count, err)
	}
	client, _ = dshHostPipe(t, frames)
	if err := client.follow(context.Background(), nil, func(json.RawMessage) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	local, peer := net.Pipe()
	defer peer.Close()
	client = &dshHostClient{dial: func(context.Context) (net.Conn, error) { return local, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.follow(ctx, nil, func(json.RawMessage) (bool, error) { return false, nil }) }()
	if _, err := bufio.NewReader(peer).ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled follow succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled subscription retained its connection")
	}
}

func TestDSHHostErrorPreservesCodeAndOperationWithoutNativeSecrets(t *testing.T) {
	client, _ := dshHostPipe(t, "{\"ok\":false,\"error\":{\"code\":\"gateway/internal\",\"reason\":\"history_corrupt\",\"message\":\"secret-token\",\"operation\":\"secret\"}}\n")
	_, err := client.call(context.Background(), "create", nil)
	if err == nil || !strings.Contains(err.Error(), "create failed [gateway/internal]") || !strings.Contains(err.Error(), "history integrity") || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	client, _ = dshHostPipe(t, "{\"ok\":false,\"error\":{\"code\":\"secret-token\"}}\n")
	_, err = client.call(context.Background(), "task.bind", nil)
	if err == nil || strings.Contains(err.Error(), "secret-token") || !strings.Contains(err.Error(), "task.bind") {
		t.Fatal(err)
	}
}
