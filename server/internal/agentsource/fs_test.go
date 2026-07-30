package agentsource

import (
	"context"
	"testing"
	"testing/fstest"
)

func TestCompileFSUsesRepositoryBundleValidation(t *testing.T) {
	source := fstest.MapFS{
		ManifestPath:                    {Data: []byte("apiVersion: multica.ai/v1alpha1\nkind: Agent\nmetadata:\n  name: Local\n  description: Embedded\nspec:\n  instructions: AGENT.md\n  skills:\n    - path: skills/example\n")},
		"AGENT.md":                      {Data: []byte("Local instructions\n")},
		"skills/example/SKILL.md":       {Data: []byte("---\nname: example\ndescription: Example skill\n---\n\nUse it.\n")},
		"skills/example/scripts/run.sh": {Data: []byte("#!/bin/sh\n")},
	}
	bundle, err := CompileFS(context.Background(), source)
	if err != nil {
		t.Fatalf("CompileFS: %v", err)
	}
	if bundle.Hash == "" || bundle.Instructions != "Local instructions\n" || len(bundle.Skills) != 1 {
		t.Fatalf("unexpected bundle: %#v", bundle)
	}
	if got := bundle.Skills[0].Files[0].Path; got != "scripts/run.sh" {
		t.Fatalf("supporting path = %q", got)
	}
}

func TestCompileDTAProjectFSUsesDTAProjectLayout(t *testing.T) {
	source := fstest.MapFS{
		DTAProjectPath: {Data: []byte(`{
			"$schema": "dingtalk-agent/project@1",
			"name": "fde-development-manager",
			"dtaVersion": "^0.1.5",
			"agent": {
				"displayName": "FDE Development Manager",
				"definition": "agent/AGENTS.md",
				"skillsRoot": "agent/skills",
				"skills": ["dta-basic-behavior", "multica-development-manager"]
			},
			"workspaces": {}
		}`)},
		"agent/AGENTS.md": {Data: []byte("DTA instructions\n")},
		"agent/skills/dta-basic-behavior/SKILL.md": {
			Data: []byte("---\nname: dta-basic-behavior\ndescription: Basic behavior\n---\n\nUse it.\n"),
		},
		"agent/skills/multica-development-manager/SKILL.md": {
			Data: []byte("---\nname: multica-development-manager\ndescription: Development manager\n---\n\nManage it.\n"),
		},
		"agent/skills/multica-development-manager/scripts/run.sh": {Data: []byte("#!/bin/sh\n")},
	}
	bundle, err := CompileDTAProjectFS(context.Background(), source)
	if err != nil {
		t.Fatalf("CompileDTAProjectFS: %v", err)
	}
	if bundle.Manifest.Metadata.Name != "FDE Development Manager" {
		t.Fatalf("name = %q", bundle.Manifest.Metadata.Name)
	}
	if bundle.Instructions != "DTA instructions\n" || len(bundle.Skills) != 2 {
		t.Fatalf("unexpected bundle: %#v", bundle)
	}
	if bundle.Skills[0].SourcePath != "agent/skills/dta-basic-behavior" ||
		bundle.Skills[1].SourcePath != "agent/skills/multica-development-manager" {
		t.Fatalf("unexpected skill paths: %#v", bundle.Skills)
	}
}

func TestCompileDTAProjectFSDoesNotFallBackToLegacyManifest(t *testing.T) {
	source := fstest.MapFS{
		ManifestPath: {Data: []byte("apiVersion: multica.ai/v1alpha1\nkind: Agent\n")},
	}
	if _, err := CompileDTAProjectFS(context.Background(), source); err == nil {
		t.Fatal("CompileDTAProjectFS accepted a repository without dingtalk-agent.json")
	}
}
