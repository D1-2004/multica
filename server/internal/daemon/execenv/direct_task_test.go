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
		if strings.Contains(got, "**Issue ID:**") || strings.Contains(got, "triggered by an Autopilot") {
			t.Fatal(got)
		}
	}
	if got := renderIssueContext("codex", ctx); got != ctx.DirectTaskPrompt+"\n" {
		t.Fatal("Direct context added platform guidance", got)
	}
}

func TestDirectTaskBriefIsExecutionOnly(t *testing.T) {
	ctx := TaskContextForEnv{
		DirectTaskPrompt: "Execute task", AgentName: "Employee",
		AgentInstructions: "ROLE_SENTINEL\nUser-authored multica issue instructions stay verbatim.\nSCENE_SENTINEL",
		WorkspaceContext:  "WORKSPACE_SENTINEL",
		AgentSkills:       []SkillContextForEnv{{Name: "domain-analysis"}},
	}
	for _, provider := range []string{"codex", "unknown"} {
		got := buildMetaSkillContent(provider, ctx)
		for _, forbidden := range []string{"# Multica Agent Runtime", "# Direct Employee Execution", "## Background Task Safety", "## Output", "## Available Commands", "## Issue", "## Workflow", "## Repositories", "## Important: Always Use", "multica attachment upload", "multica issue comment add", "run result is text-only"} {
			if strings.Contains(got, forbidden) {
				t.Errorf("%s: Direct brief leaked %q", provider, forbidden)
			}
		}
		for _, required := range []string{ctx.AgentInstructions, "WORKSPACE_SENTINEL"} {
			if !strings.Contains(got, required) {
				t.Errorf("%s: Direct brief lost %q", provider, required)
			}
		}
		if provider == "unknown" && !strings.Contains(got, "domain-analysis") {
			t.Fatal("fallback provider lost its skill index")
		}
	}
	ctx.DirectTaskPrompt = ""
	if got := buildMetaSkillContent("codex", ctx); !strings.Contains(got, "# Multica Agent Runtime") || !strings.Contains(got, "## Available Commands") {
		t.Fatal("ordinary task lost platform brief")
	}
}
