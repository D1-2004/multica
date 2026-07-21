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

func TestDispatchWorkflowPromptReachesDaemonWorkflow(t *testing.T) {
	const workflowPrompt = "DWS lifecycle: add-emoji first; reply to latest_message after delivery."
	var task Task
	if err := json.Unmarshal([]byte(`{
		"issue_id":"issue-1",
		"dispatch_workflow_prompt":"`+workflowPrompt+`",
		"dispatch_surface_type":"issue",
		"dispatch_outbound_mode":"dws"
	}`), &task); err != nil {
		t.Fatal(err)
	}

	ctx := execenv.TaskContextForEnv{
		IssueID:                  task.IssueID,
		DispatchWorkflowPrompt:   strings.TrimSpace(task.DispatchWorkflowPrompt),
		DispatchSurfaceType:      strings.TrimSpace(task.DispatchSurfaceType),
		DispatchOutboundMode:     strings.TrimSpace(task.DispatchOutboundMode),
	}
	dir := t.TempDir()
	brief, err := execenv.InjectRuntimeConfig(dir, "claude", ctx)
	if err != nil {
		t.Fatal(err)
	}
	workflowIndex := strings.Index(brief, "### Workflow")
	dispatchIndex := strings.Index(brief, "#### Dispatch Outbound Delivery")
	outputIndex := strings.Index(brief, "## Output")
	if workflowIndex < 0 || dispatchIndex < workflowIndex || outputIndex < dispatchIndex {
		t.Fatalf("dispatch outbound is not inside the daemon workflow: workflow=%d dispatch=%d output=%d\n---\n%s", workflowIndex, dispatchIndex, outputIndex, brief)
	}
	if !strings.Contains(brief, workflowPrompt) {
		t.Fatalf("runtime brief missing dispatch workflow prompt: %q", brief)
	}
	content, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), workflowPrompt) {
		t.Fatalf("sandbox workflow context missing dispatch prompt: %s", content)
	}
}
