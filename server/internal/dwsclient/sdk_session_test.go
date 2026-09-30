package dwsclient

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/dws"
)

func TestSDKEndpointsFollowTheCLIPrecedence(t *testing.T) {
	cases := []struct {
		cli          CLI
		mcp, gateway string
	}{
		{CLI{}, "https://mcp.dingtalk.com", "https://mcp-gw.dingtalk.com"},
		{CLI{MCPBaseURL: "https://pre-mcp.dingtalk.com/"}, "https://pre-mcp.dingtalk.com", "https://pre-mcp-gw.dingtalk.com"},
		// The delivery environment rewrites mcp_url for the CLI, so it wins.
		{CLI{MCPBaseURL: "https://pre-mcp.dingtalk.com", Environment: "production"}, "https://mcp.dingtalk.com", "https://mcp-gw.dingtalk.com"},
		{CLI{Environment: "staging"}, "https://pre-mcp.dingtalk.com", "https://pre-mcp-gw.dingtalk.com"},
	}
	for _, c := range cases {
		mcp, gateway, err := c.cli.sdkEndpoints()
		if err != nil || mcp != c.mcp || gateway != c.gateway {
			t.Fatalf("%+v: %s %s %v", c.cli, mcp, gateway, err)
		}
	}
	for _, bad := range []CLI{{MCPBaseURL: "http://mcp.dingtalk.com"}, {MCPBaseURL: "https://example.com"},
		{MCPBaseURL: "https://mcp.dingtalk.com/x"}, {Environment: "dev"}} {
		if _, _, err := bad.sdkEndpoints(); err == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
}

func TestSDKSessionRoundTripsPrivately(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := readSDKSession(dir); ok || err != nil {
		t.Fatalf("a CLI directory reads as SDK: %v %v", ok, err)
	}
	want := sdkSession{CorpID: "corp", UID: "42", MCP: "https://mcp.dingtalk.com", Gateway: "https://mcp-gw.dingtalk.com"}
	if err := writeSDKSession(dir, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := readSDKSession(dir)
	if !ok || err != nil || got != want {
		t.Fatalf("read %+v %v %v", got, ok, err)
	}
	info, err := os.Stat(filepath.Join(dir, sdkSessionFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", info.Mode(), err)
	}
	if err := os.WriteFile(filepath.Join(dir, sdkSessionFile), []byte(`{"mcp":"https://mcp.dingtalk.com"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := readSDKSession(dir); !ok || err == nil {
		t.Fatal("an incomplete session must not fall back to the CLI")
	}
}

func TestSDKSelectorIsLive(t *testing.T) {
	t.Cleanup(func() { SetSDKSelector(nil) })
	if sdkSelected() {
		t.Fatal("selected without a selector")
	}
	on := false
	SetSDKSelector(func() bool { return on })
	if sdkSelected() {
		t.Fatal("selected while off")
	}
	on = true
	if !sdkSelected() {
		t.Fatal("not selected while on")
	}
}

// A session that can no longer refresh ends the card consumer, so the
// decision service re-exchanges instead of keeping a dead stream "ready".
func TestSDKCardConsumerEndsWhenTheSessionExpires(t *testing.T) {
	ctx := context.Background()
	client, err := dws.NewWithToken(ctx, dws.Config{GatewayURL: "http://127.0.0.1:1", AuthURL: "http://127.0.0.1:1", SkipVerify: true},
		dws.Token{AccessToken: "t", ClientID: "c", ExpiresAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = client.Call(ctx, dws.ServerIM, "x", nil) // no refresh token: the session ends here
	if client.Alive() {
		t.Fatal("fixture session is still alive")
	}
	done := make(chan error, 1)
	go func() {
		done <- consumeCardEventsSDK(ctx, client, sdkSession{UID: "42"}, func() {}, func([]byte) error { return nil })
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "expired") || IsTimeout(err) {
			t.Fatalf("consumer ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the consumer kept running on an expired session")
	}
}

// Every Exchange drops clients of removed directories, with the switch off
// too, so their tokens do not stay in memory.
func TestExchangePrunesRemovedDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := writeSDKSession(dir, sdkSession{MCP: "https://mcp.dingtalk.com", Gateway: "https://mcp-gw.dingtalk.com"}); err != nil {
		t.Fatal(err)
	}
	sdkClients.Store(dir, &dws.Client{})
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = CLI{ClientSecret: "s", Path: filepath.Join(t.TempDir(), "missing-dws")}.Exchange(ctx, t.TempDir(), Credential{UID: "1", ClientID: "c", AuthCode: "a"})
	if _, ok := sdkClients.Load(dir); ok {
		t.Fatal("a removed directory kept its client")
	}
}
