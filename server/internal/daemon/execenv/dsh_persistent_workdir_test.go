package execenv

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDSHNativePersistentWorkspaceSurvivesTaskCleanup(t *testing.T) {
	for _, removeAll := range []bool{false, true} {
		workdir := t.TempDir()
		sentinel := filepath.Join(workdir, "employee-project.txt")
		if err := os.WriteFile(sentinel, []byte("persistent employee content"), 0600); err != nil {
			t.Fatal(err)
		}
		scratch := filepath.Join(t.TempDir(), "scratch")
		if err := os.Mkdir(scratch, 0700); err != nil {
			t.Fatal(err)
		}
		original := Environment{RootDir: scratch, WorkDir: workdir, PersistentWorkDir: true, logger: slog.Default()}
		// The execution preparer crosses a JSON subprocess boundary.
		raw, _ := json.Marshal(original)
		var restored Environment
		if err := json.Unmarshal(raw, &restored); err != nil {
			t.Fatal(err)
		}
		restored.logger = slog.Default()
		if restored.LocalDirectory || !restored.PersistentWorkDir {
			t.Fatal("persistent cloud ownership was lost or misclassified")
		}
		if err := restored.Cleanup(removeAll); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(sentinel); err != nil || string(got) != "persistent employee content" {
			t.Fatal("task cleanup deleted employee workspace")
		}
		_, err := os.Stat(scratch)
		if removeAll && !os.IsNotExist(err) {
			t.Fatal("task scratch was not cleaned")
		}
	}
}

func TestDSHNativeSkillsRefreshOutsidePersistentProject(t *testing.T) {
	workdir, scratch := t.TempDir(), t.TempDir()
	userSkill := filepath.Join(workdir, ".dsh", "skills", "employee-owned", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(userSkill), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userSkill, []byte("employee-owned skill"), 0600); err != nil {
		t.Fatal(err)
	}
	for i, revision := range []string{"first", "second", "removed"} {
		params := PrepareParams{WorkspacesRoot: scratch, WorkspaceID: "native-workspace", TaskID: []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"}[i], Provider: "opencode", PersistentWorkDir: workdir}
		params.Task.IssueID = params.TaskID
		params.Task.AgentInstructions = "Current task context revision: " + revision
		if revision != "removed" {
			params.Task.AgentSkills = []SkillContextForEnv{{Name: "native-test", Description: "Test scoped refresh", Content: revision}}
		}
		env, err := Prepare(params, slog.Default())
		if err != nil {
			t.Fatal(err)
		}
		if env.TaskSkillDirectory != filepath.Join(env.RootDir, "dsh-skills") {
			t.Fatal("Skill catalog not in task scratch")
		}
		context, err := os.ReadFile(filepath.Join(env.TaskContextDirectory, ".agent_context", "issue_context.md"))
		if err != nil || !strings.Contains(string(context), params.TaskID) {
			t.Fatalf("task metadata did not refresh: %v", err)
		}
		if _, err := os.Stat(filepath.Join(workdir, ".agent_context")); !os.IsNotExist(err) {
			t.Fatal("task metadata leaked into employee project")
		}
		brief := BuildNativeDSHContext(params.Task, env.TaskContextDirectory)
		// The brief points to current issue metadata; issue identity itself is
		// carried by that file and the per-turn prompt, not the cached brief.
		if !strings.Contains(brief, env.TaskContextDirectory) || !strings.Contains(brief, params.Task.AgentInstructions) {
			t.Fatal("dynamic context missed current task")
		}
		if revision != "removed" {
			content, err := os.ReadFile(filepath.Join(env.TaskSkillDirectory, "native-test", "SKILL.md"))
			if err != nil || !strings.Contains(string(content), revision) {
				t.Fatalf("revision not materialized: %v", err)
			}
		} else if entries, err := os.ReadDir(env.TaskSkillDirectory); err != nil || len(entries) != 0 {
			t.Fatal("removed Skill persisted into next task")
		}
		if _, err := os.Stat(filepath.Join(workdir, ".opencode", "skills")); !os.IsNotExist(err) {
			t.Fatal("task Skills leaked into persistent provider directory")
		}
		if err := env.Cleanup(true); err != nil {
			t.Fatal(err)
		}
		if content, err := os.ReadFile(userSkill); err != nil || string(content) != "employee-owned skill" {
			t.Fatal("employee Skill was modified")
		}
	}
}
