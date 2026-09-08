package dwsclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestHistoryRejectedIncludesErrorMsg(t *testing.T) {
	got := HistoryRejected("", "无权限查看会话").Error()
	if !strings.Contains(got, "operation_failed") || !strings.Contains(got, "无权限查看会话") {
		t.Fatalf("got %q", got)
	}
	if HistoryRejected("", "").Error() != "DWS conversation history query rejected: operation_failed" {
		t.Fatal("empty envelope must stay the stable placeholder")
	}
}

func TestListKeepsRejectedJSONWhenCLIExitsOne(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "dws")
	script := "#!/bin/sh\necho '{\"success\":false,\"errorCode\":null,\"errorMsg\":\"无权限查看会话\"}'\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := (CLI{Path: bin}).List(context.Background(), dir, ListRequest{ConversationID: "cid-test", Limit: 10})
	if err != nil {
		t.Fatalf("rejected JSON on stdout must be returned: %v", err)
	}
	if !strings.Contains(string(raw), "无权限查看会话") {
		t.Fatalf("raw=%s", raw)
	}
}

func TestListAttachesStderrWhenCLIExitsWithoutJSON(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "dws")
	script := "#!/bin/sh\necho boom >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := (CLI{Path: bin}).List(context.Background(), dir, ListRequest{ConversationID: "cid-test", Limit: 10})
	if err == nil || !strings.Contains(err.Error(), "DWS conversation history query failed") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("got %v", err)
	}
}

func TestListExtractsStructuredDiagnosticsBeforeClipping(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "dws")
	payload := `{"error":{"actions":["` + strings.Repeat("请联系服务端", 40) + `"],"category":"api","reason":"business_error","server_error_code":1001,"trace_id":"213d1ca017888859453646089e0906","message":"token=secret-must-not-leak","token":"secret-must-not-leak"}}`
	script := "#!/bin/sh\ncat >&2 <<'DWS_ERROR'\n" + payload + "\nDWS_ERROR\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := (CLI{Path: bin}).List(context.Background(), dir, ListRequest{ConversationID: "cid-test"})
	var detail *HistoryError
	if !errors.As(err, &detail) {
		t.Fatalf("structured history error lost: %v", err)
	}
	fields := detail.DiagnosticFields()
	for key, want := range map[string]string{"category": "api", "reason": "business_error", "server_error_code": "1001", "trace_id": "213d1ca017888859453646089e0906"} {
		if fields[key] != want || !strings.Contains(err.Error(), key+"="+want) {
			t.Errorf("missing %s in summary/fields: %v / %v", key, err, fields)
		}
	}
	if strings.Contains(err.Error(), "secret-must-not-leak") || strings.Contains(err.Error(), "请联系") {
		t.Fatalf("raw stderr leaked into diagnostics: %v", err)
	}
	malformed := historyCLIError([]byte(`{"error":{"reason":"token=never-log","trace_id":"Bearer never-log","category":"api"}}`))
	if malformed == nil || strings.Contains(malformed.Error(), "never-log") {
		t.Fatalf("malformed diagnostic values must be omitted: %v", malformed)
	}
}

func TestListPreservesDWSMillisecondContinuation(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "dws")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$DWS_CONFIG_DIR/args\"\necho '{\"success\":true}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := (CLI{Path: bin}).List(context.Background(), dir, ListRequest{
		ConversationID: "cid-test", Direction: "newer", Limit: 30,
		Before: time.Date(2026, 9, 3, 15, 43, 24, 970000000, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--time\n2026-09-03 23:43:24.970\n--direction\nnewer\n") {
		t.Fatalf("lost native continuation precision: %s", args)
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
