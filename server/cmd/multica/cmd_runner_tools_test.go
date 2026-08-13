package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/runnerprotocol"
)

func TestExecuteRunnerCallUsesServerAuthorizedRoots(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "hello.txt")
	arguments, err := json.Marshal(map[string]any{
		"path":    target,
		"content": "hello Runner",
	})
	if err != nil {
		t.Fatal(err)
	}

	result := executeRunnerCall(context.Background(), runnerprotocol.Call{
		Type:      runnerprotocol.MessageCall,
		CallID:    "call-1",
		ToolName:  "write_file",
		Arguments: arguments,
		Roots:     []string{root},
		ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339Nano),
	})
	if !result.Succeeded {
		t.Fatalf("expected call to succeed, got %s: %s", result.ErrorCode, result.ErrorMessage)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "hello Runner" {
		t.Fatalf("unexpected content %q", content)
	}
}

func TestRunnerReadFileRejectsSymlinkOutsideAuthorizedRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	arguments, _ := json.Marshal(map[string]any{"path": link})

	_, toolErr := runnerReadFile([]string{root}, arguments)
	if toolErr == nil || toolErr.code != "runner_path_outside_root" {
		t.Fatalf("expected symlink escape rejection, got %#v", toolErr)
	}
}

func TestRunnerShellWarningMatchesBehavior(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Runner shell is supported on macOS and Linux")
	}
	root := t.TempDir()
	arguments, _ := json.Marshal(map[string]any{
		"command":         "cd / && pwd",
		"cwd":             root,
		"timeout_seconds": 5,
	})

	value, toolErr := runnerShell(context.Background(), []string{root}, arguments)
	if toolErr != nil {
		t.Fatalf("unexpected shell error: %v", toolErr)
	}
	result := value.(map[string]any)
	if result["succeeded"] != true || strings.TrimSpace(result["output"].(string)) != "/" {
		t.Fatalf("expected shell to retain full OS-user access, got %#v", result)
	}
}

func TestNormalizeRunnerServerURLRequiresOrigin(t *testing.T) {
	if got, err := normalizeRunnerServerURL("https://multica.example/"); err != nil || got != "https://multica.example" {
		t.Fatalf("normalize valid origin = %q, %v", got, err)
	}
	for _, invalid := range []string{
		"",
		"ftp://multica.example",
		"https://user:secret@multica.example",
		"https://multica.example/base",
		"https://multica.example?token=secret",
	} {
		if _, err := normalizeRunnerServerURL(invalid); err == nil {
			t.Fatalf("invalid Runner origin %q accepted", invalid)
		}
	}
}

func TestDefaultRunnerDesktopUsesCurrentUserHome(t *testing.T) {
	home := t.TempDir()
	desktop := filepath.Join(home, "Desktop")
	if err := os.Mkdir(desktop, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	got, err := defaultRunnerDesktop()
	if err != nil {
		t.Fatalf("resolve default Runner root: %v", err)
	}
	if got != desktop {
		t.Fatalf("default Runner root = %q, want %q", got, desktop)
	}
}

func TestDefaultRunnerDesktopFailsWhenDesktopIsAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := defaultRunnerDesktop(); err == nil {
		t.Fatal("missing Desktop unexpectedly received a different default root")
	}
}

func TestRunnerConnectionStateTracksCurrentProcessAndMachine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const machineID = "machine-1"
	if err := writeRunnerConnectionState(machineID); err != nil {
		t.Fatalf("write connection state: %v", err)
	}
	if !runnerConnectionActive(os.Getpid(), machineID) {
		t.Fatal("current Runner connection was not recognized")
	}
	if runnerConnectionActive(os.Getpid()+1, machineID) {
		t.Fatal("connection state accepted another process")
	}
	if runnerConnectionActive(os.Getpid(), "machine-2") {
		t.Fatal("connection state accepted another machine")
	}

	clearRunnerConnectionState(os.Getpid() + 1)
	if !runnerConnectionActive(os.Getpid(), machineID) {
		t.Fatal("another process cleared the current Runner connection")
	}
	clearRunnerConnectionState(os.Getpid())
	if runnerConnectionActive(os.Getpid(), machineID) {
		t.Fatal("current Runner connection state was not cleared")
	}
}

func TestExecuteRunnerCallRejectsOversizedEncodedResult(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "control-bytes.txt")
	if err := os.WriteFile(target, bytes.Repeat([]byte{0}, 400<<10), 0o600); err != nil {
		t.Fatal(err)
	}
	arguments, _ := json.Marshal(map[string]any{"path": target})
	result := executeRunnerCall(context.Background(), runnerprotocol.Call{
		Type:      runnerprotocol.MessageCall,
		CallID:    "call-large-result",
		ToolName:  "read_file",
		Arguments: arguments,
		Roots:     []string{root},
		ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339Nano),
	})
	if result.Succeeded || result.ErrorCode != "runner_result_too_large" {
		t.Fatalf("expected encoded result limit, got %#v", result)
	}
}

func TestRunnerSearchStopsWhenCallIsCancelled(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for name, run := range map[string]func() *runnerToolError{
		"glob": func() *runnerToolError {
			_, toolErr := runnerGlob(ctx, []string{root}, json.RawMessage(`{"root":"`+root+`","pattern":"**/*"}`))
			return toolErr
		},
		"grep": func() *runnerToolError {
			_, toolErr := runnerGrep(ctx, []string{root}, json.RawMessage(`{"root":"`+root+`","pattern":"needle"}`))
			return toolErr
		},
	} {
		t.Run(name, func(t *testing.T) {
			toolErr := run()
			if toolErr == nil || toolErr.code != "runner_call_cancelled" {
				t.Fatalf("expected cancellation, got %#v", toolErr)
			}
		})
	}
}

func TestStopAllRunnerBackgroundProcessesTerminatesChildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Runner shell is supported on macOS and Linux")
	}
	root := t.TempDir()
	value, toolErr := startRunnerBackgroundShell(root, "sleep 60")
	if toolErr != nil {
		t.Fatal(toolErr)
	}
	processID := value.(map[string]any)["process_id"].(string)
	process, processErr := getRunnerBackgroundProcess(processID)
	if processErr != nil {
		t.Fatal(processErr)
	}
	t.Cleanup(func() {
		runnerProcessRegistry.Lock()
		delete(runnerProcessRegistry.items, processID)
		runnerProcessRegistry.Unlock()
	})

	stopAllRunnerBackgroundProcesses()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		process.mu.RLock()
		done := process.done
		process.mu.RUnlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("background process did not stop")
}
