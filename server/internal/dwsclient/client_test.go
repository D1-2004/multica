package dwsclient

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSafeCode(t *testing.T) {
	if SafeCode("AUTH") != "AUTH" {
		t.Fatal("passthrough")
	}
	if SafeCode("bad code!") != "operation_failed" {
		t.Fatal("reject punctuation")
	}
}

func TestCommandEnvIsolatesSecrets(t *testing.T) {
	t.Setenv("DWS_CLIENT_SECRET", "should-not-leak")
	t.Setenv("DWS_AUTH_CODE", "should-not-leak")
	env := CommandEnv("/tmp/dws-test", map[string]string{"DWS_CLIENT_ID": "abc"})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "DWS_CONFIG_DIR=/tmp/dws-test") {
		t.Fatal("config dir missing")
	}
	if !strings.Contains(joined, "DWS_CLIENT_ID=abc") {
		t.Fatal("explicit client id missing")
	}
	if strings.Contains(joined, "should-not-leak") {
		t.Fatal("blocked secret leaked into command env")
	}
}

func TestIsTimeout(t *testing.T) {
	if IsTimeout(nil) || IsTimeout(errors.New("DWS conversation history query failed")) {
		t.Fatal("plain errors are not timeouts")
	}
	if !IsTimeout(context.DeadlineExceeded) || !IsTimeout(context.Canceled) {
		t.Fatal("context errors must match")
	}
	wrapped := commandFailed(canceledCtx(t), "DWS conversation history query failed", errors.New("signal: killed"))
	if !IsTimeout(wrapped) {
		t.Fatalf("wrapped CLI timeout not detected: %v", wrapped)
	}
	plain := commandFailed(context.Background(), "DWS conversation history query failed", errors.New("exit 1"))
	if plain.Error() != "DWS conversation history query failed" {
		t.Fatalf("live CLI failure must keep stable text: %v", plain)
	}
}

func canceledCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	t.Cleanup(cancel)
	return ctx
}
