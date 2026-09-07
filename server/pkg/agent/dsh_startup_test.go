package agent

import (
	"context"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestDshExecuteBoundsStartupWrite pins the startup guard added on top of the
// upstream backend. A DSH that starts, greets, and then never drains stdin used
// to block Execute forever once the execute frame exceeded the OS pipe buffer:
// the reader and cancel goroutines do not exist yet at that point, and the
// daemon's idle watchdog only arms after Execute returns (with
// MULTICA_AGENT_TIMEOUT defaulting to 0, i.e. no deadline at all), so the task
// would hold an execution slot indefinitely.
func TestDshExecuteBoundsStartupWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	// Greet, then sleep without ever reading stdin.
	bin := writeDshFixture(t, `
printf '%s\n' '{"v":1,"type":"ready","runtime":"dsh","plugin_version":"test","capabilities":{}}'
sleep 60
`)
	original := dshStartupWriteTimeout
	dshStartupWriteTimeout = 300 * time.Millisecond
	t.Cleanup(func() { dshStartupWriteTimeout = original })

	b, err := New("dsh", Config{ExecutablePath: bin, TaskID: "task-stuck", Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	// A prompt comfortably larger than a pipe buffer, so the write cannot
	// complete until the child reads it.
	prompt := strings.Repeat("multica-dsh-startup-guard ", 40000)

	done := make(chan error, 1)
	go func() {
		_, execErr := b.Execute(context.Background(), prompt, ExecOptions{Cwd: t.TempDir()})
		done <- execErr
	}()

	select {
	case execErr := <-done:
		if execErr == nil {
			t.Fatal("expected Execute to fail when dsh never reads the execute command")
		}
		if !strings.Contains(execErr.Error(), "did not read the execute command") {
			t.Fatalf("unexpected startup error: %v", execErr)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Execute blocked on the startup write instead of failing closed")
	}
}

// TestDshDeadlineReportsTimeoutNotCancelled pins the terminal-status fix. When
// a positive Timeout expires, the backend sends the same cancel frame a user
// cancellation sends, and a protocol-abiding DSH answers with a terminal result
// of status "cancelled". Reporting that verbatim would tell the server the task
// was cancelled by a person and drop failure_reason="timeout".
func TestDshDeadlineReportsTimeoutNotCancelled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin := writeDshFixture(t, `
printf '%s\n' '{"v":1,"type":"ready","runtime":"dsh","plugin_version":"test","capabilities":{}}'
IFS= read -r execute
printf '%s\n' '{"v":1,"type":"session","request_id":"task-deadline","session_id":"session-deadline","resumed":false}'
IFS= read -r cancel
case "$cancel" in *'"type":"cancel"'*) ;; *) exit 8 ;; esac
printf '%s\n' '{"v":1,"type":"usage","request_id":"task-deadline","provider":"deap","model":"m","input_tokens":7,"output_tokens":3}'
printf '%s\n' '{"v":1,"type":"result","request_id":"task-deadline","status":"cancelled","session_id":"session-deadline","output":"partial","resume_rejected":false}'
`)
	b, err := New("dsh", Config{ExecutablePath: bin, TaskID: "task-deadline", Logger: slog.Default()})
	if err != nil {
		t.Fatal(err)
	}
	session, err := b.Execute(context.Background(), "work", ExecOptions{
		Cwd:     t.TempDir(),
		Timeout: 400 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range session.Messages {
	}
	result := <-session.Result
	if result.Status != "timeout" {
		t.Fatalf("expected a deadline to report timeout, got %q (error=%q)", result.Status, result.Error)
	}
	if result.SessionID != "session-deadline" {
		t.Fatalf("timeout rewrite lost the session id: %#v", result)
	}
	if usage, ok := result.Usage["deap/m"]; !ok || usage.InputTokens != 7 {
		t.Fatalf("timeout rewrite lost token usage: %#v", result.Usage)
	}
	if result.Output != "partial" {
		t.Fatalf("timeout rewrite lost the partial output: %#v", result)
	}
}
