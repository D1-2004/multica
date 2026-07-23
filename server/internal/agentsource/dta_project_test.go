package agentsource

import (
	"context"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/githubapp"
)

const validDTAProject = `{
  "$schema": "dingtalk-agent/project@1",
  "name": "fde-development-manager",
  "dtaVersion": "^0.1.5",
  "agent": {
    "displayName": "FDE Development Manager",
    "definition": "AGENT.md",
    "skills": [
      "dingtalk-basic-behavior",
      "multica-development-manager"
    ]
  },
  "workspaces": {}
}`

func TestParseDTAProjectValidatesStableCoreAndIgnoresExtensions(t *testing.T) {
	content := strings.Replace(
		validDTAProject,
		`"workspaces": {}`,
		`"workspaces": {"future": {"shape": "owned-by-dta"}},
  "agentPlatform": "future-platform",
  "futureProjectField": {"enabled": true}`,
		1,
	)
	content = strings.Replace(
		content,
		`"definition": "AGENT.md",`,
		`"definition": "AGENT.md",
    "futureAgentField": true,`,
		1,
	)

	project, err := ParseDTAProject([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if project.Schema != DTAProjectSchema || project.Name != "fde-development-manager" {
		t.Fatalf("unexpected project: %+v", project)
	}
	if project.Agent.DisplayName != "FDE Development Manager" ||
		project.Agent.Definition != "AGENT.md" ||
		len(project.Agent.Skills) != 2 {
		t.Fatalf("unexpected agent: %+v", project.Agent)
	}
}

func TestParseDTAProjectRejectsInvalidStableCore(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "unsupported schema",
			content: strings.Replace(validDTAProject, DTAProjectSchema, "dingtalk-agent/project@2", 1),
			want:    "unsupported project protocol",
		},
		{
			name:    "invalid project name",
			content: strings.Replace(validDTAProject, "fde-development-manager", "FDE Manager", 1),
			want:    "stable name",
		},
		{
			name:    "missing DTA version",
			content: strings.Replace(validDTAProject, "^0.1.5", " ", 1),
			want:    "dtaVersion",
		},
		{
			name:    "unsafe definition",
			content: strings.Replace(validDTAProject, "AGENT.md", "../AGENT.md", 1),
			want:    "unsafe path",
		},
		{
			name:    "null display name",
			content: strings.Replace(validDTAProject, `"FDE Development Manager"`, "null", 1),
			want:    "displayName must be a string",
		},
		{
			name: "missing basic skill",
			content: strings.Replace(
				validDTAProject,
				`"dingtalk-basic-behavior",`,
				"",
				1,
			),
			want: DTABasicSkill,
		},
		{
			name:    "workspaces is not an object",
			content: strings.Replace(validDTAProject, `"workspaces": {}`, `"workspaces": []`, 1),
			want:    "workspaces must be an object",
		},
		{
			name:    "trailing JSON value",
			content: validDTAProject + "\n{}",
			want:    "exactly one JSON value",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseDTAProject([]byte(test.content))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestCompileDTAProjectMapsDefinitionAndSkillLocations(t *testing.T) {
	repository := fakeRepository{
		tree: githubapp.Tree{Entries: []githubapp.TreeEntry{
			{Path: DTAProjectPath, Type: "blob", Mode: "100644", SHA: "project", Size: int64(len(validDTAProject))},
			{Path: "AGENT.md", Type: "blob", Mode: "100644", SHA: "definition", Size: 12},
			{Path: ".agents/skills/dingtalk-basic-behavior/SKILL.md", Type: "blob", Mode: "100644", SHA: "basic", Size: 80},
			{Path: "skills/multica-development-manager/SKILL.md", Type: "blob", Mode: "100644", SHA: "role", Size: 80},
			{Path: "skills/multica-development-manager/references/process.md", Type: "blob", Mode: "100644", SHA: "reference", Size: 20},
		}},
		blobs: map[string][]byte{
			"project":    []byte(validDTAProject),
			"definition": []byte("Manage work"),
			"basic":      []byte("---\nname: dingtalk-basic-behavior\ndescription: Shared behavior\n---\n# Basic"),
			"role":       []byte("---\nname: multica-development-manager\ndescription: Manage Multica\n---\n# Role"),
			"reference":  []byte("# Process"),
		},
	}

	bundle, err := CompileDTAProject(
		context.Background(),
		repository,
		Source{InstallationID: 1, Owner: "acme", Repository: "agent", CommitSHA: "abc"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.Metadata.Name != "FDE Development Manager" {
		t.Fatalf("name = %q", bundle.Manifest.Metadata.Name)
	}
	if bundle.Manifest.Spec.Instructions != "AGENT.md" || bundle.Instructions != "Manage work" {
		t.Fatalf("unexpected instructions: %+v", bundle)
	}
	if len(bundle.Manifest.Spec.Compatibility.Providers) != 0 {
		t.Fatalf("DTA project must not synthesize provider restrictions: %v", bundle.Manifest.Spec.Compatibility.Providers)
	}
	if len(bundle.Skills) != 2 {
		t.Fatalf("skills = %+v", bundle.Skills)
	}
	if bundle.Skills[0].SourcePath != ".agents/skills/dingtalk-basic-behavior" ||
		bundle.Skills[1].SourcePath != "skills/multica-development-manager" {
		t.Fatalf("unexpected skill paths: %+v", bundle.Skills)
	}
	if len(bundle.Skills[1].Files) != 1 || bundle.Skills[1].Files[0].Path != "references/process.md" {
		t.Fatalf("unexpected role files: %+v", bundle.Skills[1].Files)
	}
}

func TestCompileDTAProjectRequiresDeclaredSkillNameToMatchFrontmatter(t *testing.T) {
	project := strings.Replace(
		validDTAProject,
		",\n      \"multica-development-manager\"",
		"",
		1,
	)
	repository := fakeRepository{
		tree: githubapp.Tree{Entries: []githubapp.TreeEntry{
			{Path: DTAProjectPath, Type: "blob", Mode: "100644", SHA: "project", Size: int64(len(project))},
			{Path: "AGENT.md", Type: "blob", Mode: "100644", SHA: "definition", Size: 12},
			{Path: ".agents/skills/dingtalk-basic-behavior/SKILL.md", Type: "blob", Mode: "100644", SHA: "basic", Size: 80},
		}},
		blobs: map[string][]byte{
			"project":    []byte(project),
			"definition": []byte("Manage work"),
			"basic":      []byte("---\nname: wrong-name\n---\n# Basic"),
		},
	}

	_, err := CompileDTAProject(
		context.Background(),
		repository,
		Source{InstallationID: 1, Owner: "acme", Repository: "agent", CommitSHA: "abc"},
	)
	if err == nil || !strings.Contains(err.Error(), "expected \"dingtalk-basic-behavior\"") {
		t.Fatalf("error = %v", err)
	}
}

func TestCompileDTAProjectDoesNotFallBackToLegacyManifest(t *testing.T) {
	legacyManifest := `apiVersion: multica.ai/v1alpha1
kind: Agent
metadata: {name: reviewer}
spec:
  instructions: AGENT.md
`
	repository := fakeRepository{
		tree: githubapp.Tree{Entries: []githubapp.TreeEntry{
			{Path: ManifestPath, Type: "blob", Mode: "100644", SHA: "manifest", Size: int64(len(legacyManifest))},
			{Path: "AGENT.md", Type: "blob", Mode: "100644", SHA: "definition", Size: 12},
		}},
		blobs: map[string][]byte{
			"manifest":   []byte(legacyManifest),
			"definition": []byte("Manage work"),
		},
	}

	_, err := CompileDTAProject(
		context.Background(),
		repository,
		Source{InstallationID: 1, Owner: "acme", Repository: "agent", CommitSHA: "abc"},
	)
	if err == nil || !strings.Contains(err.Error(), `required file "dingtalk-agent.json" was not found`) {
		t.Fatalf("error = %v", err)
	}
}
