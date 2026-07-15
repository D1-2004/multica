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
