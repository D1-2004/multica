package execenv

import (
	"strings"
	"testing"
)

func TestDirectTaskBriefAndContext(t *testing.T) {
	ctx := TaskContextForEnv{DirectTaskPrompt: "Return DIRECT_OK", AgentName: "Employee", AgentID: "agent-1"}
	if classifyTask(ctx).hasIssueContext() {
		t.Fatal("Direct acquired issue workflow")
	}
	for _, got := range []string{buildMetaSkillContent("codex", ctx), renderIssueContext("codex", ctx)} {
		if !strings.Contains(got, "Direct") || strings.Contains(got, "**Issue ID:**") || strings.Contains(got, "triggered by an Autopilot") {
			t.Fatal(got)
		}
	}
}
