package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
)

func TestDispatchRuntimePromptReachesDaemonSystemContext(t *testing.T) {
	const runtimePrompt = "Private dispatch rule: use DWS for the final DingTalk reply."
	var task Task
	if err := json.Unmarshal([]byte(`{"issue_id":"issue-1","dispatch_runtime_prompt":"`+runtimePrompt+`"}`), &task); err != nil {
		t.Fatal(err)
	}

	ctx := execenv.TaskContextForEnv{
		IssueID:               task.IssueID,
		DispatchRuntimePrompt: dispatchRuntimePromptForEnv(task),
	}
	dir := t.TempDir()
	brief, err := execenv.InjectRuntimeConfig(dir, "claude", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(brief, runtimePrompt) {
		t.Fatalf("runtime brief missing dispatch prompt: %q", brief)
	}
	content, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), runtimePrompt) {
		t.Fatalf("sandbox system context missing dispatch prompt: %s", content)
	}
}
