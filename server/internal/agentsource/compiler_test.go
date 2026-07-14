package agentsource

import (
	"context"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/githubapp"
)

type fakeRepository struct {
	tree  githubapp.Tree
	blobs map[string][]byte
}

func (f fakeRepository) GetTree(context.Context, int64, string, string, string) (githubapp.Tree, error) {
	return f.tree, nil
}

func (f fakeRepository) GetBlob(_ context.Context, _ int64, _, _, sha string) ([]byte, error) {
	return f.blobs[sha], nil
}

func TestParseManifestStrictValidation(t *testing.T) {
	valid := `apiVersion: multica.ai/v1alpha1
kind: Agent
metadata:
  name: reviewer
  description: Reviews code
spec:
  instructions: AGENT.md
  skills:
    - path: skills/review
  compatibility:
    providers: [claude, codex]
`
	if _, err := ParseManifest([]byte(valid)); err != nil {
		t.Fatalf("valid manifest: %v", err)
	}

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"unknown field", valid + "unknown: true\n", "field unknown not found"},
		{"unsafe instructions", strings.Replace(valid, "AGENT.md", "../AGENT.md", 1), "unsafe path"},
		{"duplicate skill", strings.Replace(valid, "  compatibility:", "    - path: skills/review\n  compatibility:", 1), "duplicate skill path"},
		{"unknown provider", strings.Replace(valid, "codex", "future-cli", 1), "unknown compatibility provider"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(test.content))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestCompileMaterializesTextAndSkipsBinarySupportingFiles(t *testing.T) {
	manifest := `apiVersion: multica.ai/v1alpha1
kind: Agent
metadata:
  name: reviewer
  description: Reviews code
spec:
  instructions: AGENT.md
  skills:
    - path: skills/review
`
	repository := fakeRepository{
		tree: githubapp.Tree{Entries: []githubapp.TreeEntry{
			{Path: ManifestPath, Type: "blob", Mode: "100644", SHA: "manifest", Size: int64(len(manifest))},
			{Path: "AGENT.md", Type: "blob", Mode: "100644", SHA: "instructions", Size: 12},
			{Path: "skills/review/SKILL.md", Type: "blob", Mode: "100644", SHA: "skill", Size: 60},
			{Path: "skills/review/scripts/check.sh", Type: "blob", Mode: "100755", SHA: "script", Size: 20},
			{Path: "skills/review/icon.png", Type: "blob", Mode: "100644", SHA: "binary", Size: 4},
		}},
		blobs: map[string][]byte{
			"manifest":     []byte(manifest),
			"instructions": []byte("Review safely"),
			"skill":        []byte("---\nname: code-review\ndescription: Review code\n---\n# Review"),
			"script":       []byte("#!/bin/sh\necho check\n"),
			"binary":       {0, 1, 2, 3},
		},
	}
	bundle, err := Compile(context.Background(), repository, Source{InstallationID: 1, Owner: "acme", Repository: "agent", CommitSHA: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Instructions != "Review safely" || len(bundle.Skills) != 1 {
		t.Fatalf("unexpected bundle: %+v", bundle)
	}
	if len(bundle.Skills[0].Files) != 1 || bundle.Skills[0].Files[0].Path != "scripts/check.sh" {
		t.Fatalf("unexpected files: %+v", bundle.Skills[0].Files)
	}
	if len(bundle.Warnings) != 1 || !strings.Contains(bundle.Warnings[0], "icon.png") {
		t.Fatalf("unexpected warnings: %v", bundle.Warnings)
	}
	if len(bundle.Hash) != 64 {
		t.Fatalf("hash = %q", bundle.Hash)
	}
}

func TestCompileNormalizesOptionalCollections(t *testing.T) {
	manifest := `apiVersion: multica.ai/v1alpha1
kind: Agent
metadata:
  name: reviewer
spec:
  instructions: AGENT.md
`
	repository := fakeRepository{
		tree: githubapp.Tree{Entries: []githubapp.TreeEntry{
			{Path: ManifestPath, Type: "blob", Mode: "100644", SHA: "manifest", Size: int64(len(manifest))},
			{Path: "AGENT.md", Type: "blob", Mode: "100644", SHA: "instructions", Size: 4},
		}},
		blobs: map[string][]byte{
			"manifest": []byte(manifest), "instructions": []byte("work"),
		},
	}

	bundle, err := Compile(context.Background(), repository, Source{InstallationID: 1, Owner: "acme", Repository: "agent", CommitSHA: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Skills == nil {
		t.Fatal("skills must serialize as an empty array, not null")
	}
	if bundle.Warnings == nil {
		t.Fatal("warnings must serialize as an empty array, not null")
	}
	if bundle.Manifest.Spec.Compatibility.Providers == nil {
		t.Fatal("compatible providers must serialize as an empty array, not null")
	}
}

func TestCompileRejectsSymlinkAndLFS(t *testing.T) {
	baseManifest := `apiVersion: multica.ai/v1alpha1
kind: Agent
metadata: {name: reviewer}
spec:
  instructions: AGENT.md
  skills:
    - path: skills/review
`
	tests := []struct {
		name  string
		entry githubapp.TreeEntry
		blob  []byte
		want  string
	}{
		{"symlink", githubapp.TreeEntry{Path: "skills/review/link", Type: "blob", Mode: "120000", SHA: "extra", Size: 3}, []byte("foo"), "unsupported git object"},
		{"lfs", githubapp.TreeEntry{Path: "skills/review/model.bin", Type: "blob", Mode: "100644", SHA: "extra", Size: 50}, []byte("version https://git-lfs.github.com/spec/v1\n"), "Git LFS pointer"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := fakeRepository{
				tree: githubapp.Tree{Entries: []githubapp.TreeEntry{
					{Path: ManifestPath, Type: "blob", Mode: "100644", SHA: "manifest", Size: int64(len(baseManifest))},
					{Path: "AGENT.md", Type: "blob", Mode: "100644", SHA: "instructions", Size: 4},
					{Path: "skills/review/SKILL.md", Type: "blob", Mode: "100644", SHA: "skill", Size: 20},
					test.entry,
				}},
				blobs: map[string][]byte{
					"manifest": []byte(baseManifest), "instructions": []byte("work"),
					"skill": []byte("# Review"), "extra": test.blob,
				},
			}
			_, err := Compile(context.Background(), repository, Source{InstallationID: 1, Owner: "acme", Repository: "agent", CommitSHA: "abc"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestBundleHashIncludesManifestMetadata(t *testing.T) {
	bundle := Bundle{
		Manifest: Manifest{
			APIVersion: APIVersion,
			Kind:       Kind,
			Metadata:   ManifestMetadata{Name: "reviewer", Description: "Reviews code"},
			Spec:       ManifestSpec{Instructions: "AGENT.md"},
		},
		Instructions: "Review safely",
	}
	original := hashBundle(bundle)
	bundle.Manifest.Metadata.Description = "Reviews code and tests"
	if changed := hashBundle(bundle); changed == original {
		t.Fatal("bundle hash did not change when manifest metadata changed")
	}
}

func TestCompileRejectsDuplicateSkillNames(t *testing.T) {
	manifest := `apiVersion: multica.ai/v1alpha1
kind: Agent
metadata: {name: reviewer}
spec:
  instructions: AGENT.md
  skills:
    - path: skills/first
    - path: skills/second
`
	repository := fakeRepository{
		tree: githubapp.Tree{Entries: []githubapp.TreeEntry{
			{Path: ManifestPath, Type: "blob", Mode: "100644", SHA: "manifest", Size: int64(len(manifest))},
			{Path: "AGENT.md", Type: "blob", Mode: "100644", SHA: "instructions", Size: 4},
			{Path: "skills/first/SKILL.md", Type: "blob", Mode: "100644", SHA: "first", Size: 30},
			{Path: "skills/second/SKILL.md", Type: "blob", Mode: "100644", SHA: "second", Size: 30},
		}},
		blobs: map[string][]byte{
			"manifest": []byte(manifest), "instructions": []byte("work"),
			"first":  []byte("---\nname: shared\n---\n# First"),
			"second": []byte("---\nname: shared\n---\n# Second"),
		},
	}
	_, err := Compile(context.Background(), repository, Source{InstallationID: 1, Owner: "acme", Repository: "agent", CommitSHA: "abc"})
	if err == nil || !strings.Contains(err.Error(), "same name") {
		t.Fatalf("error = %v, want duplicate skill name error", err)
	}
}
