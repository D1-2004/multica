package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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

func TestRunnerConfigBindingsAcceptLegacyAndMultipleServers(t *testing.T) {
	legacy := runnerConfig{
		ServerURL: "https://pre-fde-workbench.dingtalk.com/",
		MachineID: "machine-pre",
	}
	bindings, err := legacy.bindings()
	if err != nil {
		t.Fatalf("legacy bindings: %v", err)
	}
	if len(bindings) != 1 || bindings[0].ServerURL != "https://pre-fde-workbench.dingtalk.com" || bindings[0].MachineID != "machine-pre" {
		t.Fatalf("legacy bindings = %#v", bindings)
	}

	cfg := runnerConfig{}
	if err := cfg.upsertBinding("https://pre-fde-workbench.dingtalk.com", "machine-pre"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.upsertBinding("https://fde-workbench.dingtalk.com", "machine-prod"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.upsertBinding("https://pre-fde-workbench.dingtalk.com/", "machine-pre-2"); err != nil {
		t.Fatal(err)
	}
	bindings, err = cfg.bindings()
	if err != nil {
		t.Fatalf("multi bindings: %v", err)
	}
	if len(bindings) != 2 {
		t.Fatalf("binding count = %d, want 2", len(bindings))
	}
	if bindings[0].ServerURL != "https://pre-fde-workbench.dingtalk.com" || bindings[0].MachineID != "machine-pre-2" {
		t.Fatalf("updated pre binding = %#v", bindings[0])
	}
	if bindings[1].ServerURL != "https://fde-workbench.dingtalk.com" || bindings[1].MachineID != "machine-prod" {
		t.Fatalf("prod binding = %#v", bindings[1])
	}
	if cfg.ServerURL != "" || cfg.MachineID != "" {
		t.Fatalf("legacy fields should be cleared after upsert: %#v", cfg)
	}
	if _, found, err := cfg.bindingForURL("https://fde-workbench.dingtalk.com"); err != nil || !found {
		t.Fatalf("prod binding lookup failed: found=%v err=%v", found, err)
	}
	if _, found, err := cfg.bindingForURL("https://example.invalid"); err != nil || found {
		t.Fatalf("unknown server unexpectedly found: found=%v err=%v", found, err)
	}
}

func TestRunnerConnectionStateTracksMultipleMachines(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := writeRunnerConnectionState("machine-1"); err != nil {
		t.Fatal(err)
	}
	if err := writeRunnerConnectionState("machine-2"); err != nil {
		t.Fatal(err)
	}
	if !runnerConnectionActive(os.Getpid(), "machine-1") || !runnerConnectionActive(os.Getpid(), "machine-2") {
		t.Fatal("both Runner connections should be active")
	}
	cfg := runnerConfig{Servers: []runnerServerBinding{
		{ServerURL: "https://pre.example", MachineID: "machine-1"},
		{ServerURL: "https://prod.example", MachineID: "machine-2"},
	}}
	if !runnerAllBindingsConnected(os.Getpid(), cfg) {
		t.Fatal("all bindings should be connected")
	}
	clearRunnerConnectionMachine(os.Getpid(), "machine-1")
	if runnerConnectionActive(os.Getpid(), "machine-1") {
		t.Fatal("cleared machine should be offline")
	}
	if !runnerConnectionActive(os.Getpid(), "machine-2") {
		t.Fatal("remaining machine should stay online")
	}
	if runnerAllBindingsConnected(os.Getpid(), cfg) {
		t.Fatal("partial connection should not count as fully online")
	}
	clearRunnerConnectionMachine(os.Getpid(), "machine-2")
	if runnerConnectionActive(os.Getpid(), "machine-2") {
		t.Fatal("last machine should be cleared")
	}
}

func TestRunnerConnectionStaysOnlineWithoutAgentMounts(t *testing.T) {
	connected := make(chan struct{}, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/runner/machines/machine-1/challenges":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"challenge_id":"challenge-1","challenge":"sign-me","active_binding_count":0}`))
		case "/api/runner/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			connected <- struct{}{}
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err = runRunnerConnection(ctx, runnerServerBinding{ServerURL: server.URL, MachineID: "machine-1"}, privateKey, runnerprotocol.MCPInventory{Type: runnerprotocol.MessageInventory, Servers: []runnerprotocol.MCPServerSummary{}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if errors.Is(err, errRunnerHasNoBindings) {
		t.Fatal("zero mounts stopped the Runner connection")
	}
	select {
	case <-connected:
	default:
		t.Fatal("Runner did not open its WebSocket with zero mounts")
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
