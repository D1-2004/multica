package main

import (
	"os"
	"testing"

	"github.com/multica-ai/multica/server/pkg/runtimeconfig"
)

func TestAppRuntimeConfigReadsCurrentRuntimeProviderSnapshot(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/runtime-config.example.json")
	if err != nil {
		t.Fatalf("read runtime config example: %v", err)
	}
	cfg, err := runtimeconfig.ParseStrict(raw, true)
	if err != nil {
		t.Fatalf("parse runtime config example: %v", err)
	}
	remote, err := runtimeconfig.NewStatic(cfg)
	if err != nil {
		t.Fatalf("new static runtime config: %v", err)
	}

	first, err := remote.ApplyRuntimeProvidersJSON([]byte(`{
	  "version": 1,
	  "fc_templates": {
	    "template-1": {"runtime_commit":"0e70a766342698a90e70a766342698a90e70a766","providers":["hermes","opencode"]}
	  },
	  "asb_commits": {}
}`))
	if err != nil {
		t.Fatalf("apply initial Runtime providers: %v", err)
	}
	app := &appRuntimeConfig{remote: remote}
	initial := app.fce2b()
	if got := initial.FCTemplateProviders["template-1"].Providers; len(got) != 2 || got[1] != "opencode" {
		t.Fatalf("initial providers = %#v", got)
	}

	entry := initial.FCTemplateProviders["template-1"]
	entry.Providers[0] = "mutated"
	initial.FCTemplateProviders["template-1"] = entry
	if got := remote.RuntimeProviders().FCTemplates["template-1"].Providers[0]; got != "hermes" {
		t.Fatalf("caller mutated service snapshot: %q", got)
	}

	second, err := remote.ApplyRuntimeProvidersJSON([]byte(`{
	  "version": 1,
	  "fc_templates": {},
	  "asb_commits": {
	    "ac20d08b3a999731ac20d08b3a999731ac20d08b": ["pi"]
	  }
}`))
	if err != nil {
		t.Fatalf("apply updated Runtime providers: %v", err)
	}
	if second.Generation != first.Generation+1 {
		t.Fatalf("generation = %d, want %d", second.Generation, first.Generation+1)
	}
	updated := app.fce2b()
	if got := updated.ASBCommitProviders["ac20d08b3a999731ac20d08b3a999731ac20d08b"]; len(got) != 1 || got[0] != "pi" {
		t.Fatalf("updated providers = %#v", got)
	}
	if _, ok := updated.FCTemplateProviders["template-1"]; ok {
		t.Fatal("app runtime config retained removed provider key")
	}
}
