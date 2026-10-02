package daemon

import (
	"context"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestDirectTaskPromptAndCapability(t *testing.T) {
	task := Task{ID: "queue-1", WorkspaceID: "workspace-1", DirectTaskPrompt: "Analyze current order queue and return exact marker DIRECT_OK"}
	p := BuildPrompt(task, "codex")
	if !strings.Contains(p, task.DirectTaskPrompt) || !strings.Contains(p, "captured automatically") {
		t.Fatal(p)
	}
	for _, bad := range []string{"Start by running `multica issue get", "multica issue create", "Autopilot"} {
		if strings.Contains(p, bad) {
			t.Fatal("wrong execution mode", p)
		}
	}
	if !strings.Contains(daemonClientCapabilities(), protocol.DaemonCapabilityEmployeeDirectV1) {
		t.Fatal("Direct capability missing")
	}
	ordinary := BuildPrompt(Task{IssueID: "issue-1"}, "codex")
	if !strings.Contains(ordinary, "multica issue get issue-1") {
		t.Fatal(ordinary)
	}
}

func TestDirectTaskRunsFakeBinaryWithExactPrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake executable")
	}
	d, _, closeServer := newLeaderReuseTestDaemon(t)
	defer closeServer()
	capture := filepath.Join(t.TempDir(), "input.json")
	script := `#!/bin/sh
IFS= read -r input
printf '%s\n' "$input" > "$DIRECT_CAPTURE"
printf '%s\n' '{"type":"system","session_id":"direct-session"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"direct-session","result":"DIRECT_EXECUTED"}'
`
	if err := os.WriteFile(d.cfg.Agents["claude"].Path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	task := Task{ID: "direct-fake-run", WorkspaceID: "direct-workspace", RuntimeID: "rt-leader", DirectTaskPrompt: "Return DIRECT_EXECUTED after checking the provided task", AuthToken: "mat_direct_fixture", Agent: &AgentData{ID: "direct-agent", Name: "Direct Employee", CustomEnv: map[string]string{"DIRECT_CAPTURE": capture}}}
	result, err := d.runTask(context.Background(), task, "claude", 0, d.logger)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.Comment != "DIRECT_EXECUTED" {
		t.Fatalf("unexpected result %+v", result)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), task.DirectTaskPrompt) || strings.Contains(string(raw), "multica issue get") {
		t.Fatalf("wrong provider input: %s", raw)
	}
	brief, err := os.ReadFile(filepath.Join(result.WorkDir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(brief), "Direct employee task") {
		t.Fatalf("wrong runtime brief: %s", brief)
	}
}

// The r2 image build gate runs this test without a database or agent account.
// It checks real HTTP headers and decoding, not just the capability constant.
func TestEmployeeDirectClientTransport(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer build-test-token" {
			t.Error("client authorization header missing")
		}
		found := false
		for _, cap := range strings.Split(r.Header.Get("X-Client-Capabilities"), ",") {
			if strings.TrimSpace(cap) == protocol.DaemonCapabilityEmployeeDirectV1 {
				found = true
			}
		}
		if !found {
			t.Errorf("%s did not advertise employee-direct-v1", r.URL.Path)
		}
		mu.Lock()
		seen[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"task":{"id":"direct-task","workspace_id":"build-workspace","direct_task_prompt":"BUILD_DIRECT_OK"},"tasks":[{"id":"direct-task","workspace_id":"build-workspace","direct_task_prompt":"BUILD_DIRECT_OK"}]}`)
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	c.SetToken("build-test-token")
	ctx := context.Background()
	if _, err := c.Register(ctx, map[string]any{"daemon_id": "build-test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SendHeartbeat(ctx, "build-runtime"); err != nil {
		t.Fatal(err)
	}
	task, err := c.ClaimTask(ctx, "build-runtime")
	if err != nil || task == nil || task.DirectTaskPrompt != "BUILD_DIRECT_OK" {
		t.Fatal(task, err)
	}
	tasks, err := c.ClaimTasks(ctx, "build-test", []string{"build-runtime"}, 1)
	if err != nil || len(tasks) != 1 || tasks[0].DirectTaskPrompt != "BUILD_DIRECT_OK" {
		t.Fatal(tasks, err)
	}
	for _, path := range []string{"/api/daemon/register", "/api/daemon/heartbeat", "/api/daemon/runtimes/build-runtime/tasks/claim", "/api/daemon/tasks/claim"} {
		mu.Lock()
		n := seen[path]
		mu.Unlock()
		if n != 1 {
			t.Fatalf("%s requests=%d", path, n)
		}
	}
}
