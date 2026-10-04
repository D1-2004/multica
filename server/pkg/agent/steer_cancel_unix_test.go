//go:build unix

package agent

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The leader closes stdout and exits on TERM, while its detached tool ignores
// TERM. A published cancellation result must already have joined that tool.
func TestOpenCodeCancellationResultWaitsForDetachedDescendant(t *testing.T) {
	opencodeTerminateGraceNanos.Store(int64(200 * time.Millisecond))
	t.Cleanup(func() { opencodeTerminateGraceNanos.Store(0) })
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pids")
	fake := filepath.Join(dir, "opencode")
	script := strings.ReplaceAll(claudeMixedSignalFakeScript(), `{"type":"system","session_id":"ses_fake"}`, `{"type":"step_start","sessionID":"ses_fake"}`)
	writeTestExecutable(t, fake, []byte(script))
	backend, err := New("opencode", Config{ExecutablePath: fake, Logger: slog.Default(), Env: map[string]string{"CLAUDE_PID_FILE": pidFile}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session, err := backend.Execute(ctx, "prompt", ExecOptions{Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for range session.Messages {
		}
	}()
	pids := waitForPids(t, pidFile)
	cancel()
	select {
	case result := <-session.Result:
		if !result.ProcessGroupStopped {
			t.Fatal("result lacks process-group exit proof")
		}
		for _, pid := range pids {
			waitProcessGone(t, pid)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("provider cleanup did not return")
	}
}
