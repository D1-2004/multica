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
	  "providers": ["hermes", "opencode"]
}`))
	if err != nil {
		t.Fatalf("apply initial Runtime providers: %v", err)
	}
	app := &appRuntimeConfig{remote: remote}
	initial := app.fce2b()
	if got := initial.RuntimeProviders; len(got) != 2 || got[1] != "opencode" {
		t.Fatalf("initial providers = %#v", got)
	}

	initial.RuntimeProviders[0] = "mutated"
	if got := remote.RuntimeProviders().Providers[0]; got != "hermes" {
		t.Fatalf("caller mutated service snapshot: %q", got)
	}

	second, err := remote.ApplyRuntimeProvidersJSON([]byte(`{
	  "version": 1,
	  "providers": ["pi"]
}`))
	if err != nil {
		t.Fatalf("apply updated Runtime providers: %v", err)
	}
	if second.Generation != first.Generation+1 {
		t.Fatalf("generation = %d, want %d", second.Generation, first.Generation+1)
	}
	updated := app.fce2b()
	if got := updated.RuntimeProviders; len(got) != 1 || got[0] != "pi" {
		t.Fatalf("updated providers = %#v", got)
	}
}
