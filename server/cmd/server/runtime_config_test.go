package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/runtimeconfig"
)

func TestAppRuntimeConfigDSHAuthorityDoesNotUseTaskRelay(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/runtime-config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := runtimeconfig.ParseStrict(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Runtime.FCE2B.ServerURL = "https://production-relay.test"
	cfg.Web.AppURL = "https://pre.multica.test"
	remote, err := runtimeconfig.NewStatic(cfg)
	if err != nil {
		t.Fatal(err)
	}
	app := &appRuntimeConfig{remote: remote}
	if got := app.fce2b(); got.AppOrigin != cfg.Web.AppURL || got.ServerURL != cfg.Runtime.FCE2B.ServerURL {
		t.Fatal("DSH authority and task relay were conflated")
	}
	cfg.Web.AppURL = "https://updated-pre.multica.test"
	updated, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.ApplyJSON(updated); err != nil {
		t.Fatal(err)
	}
	if got := app.fce2b(); got.AppOrigin != cfg.Web.AppURL || got.ServerURL != cfg.Runtime.FCE2B.ServerURL {
		t.Fatal("DSH authority did not follow the current app origin")
	}
}

func TestAppRuntimeConfigReadsCurrentSiteConnectSrc(t *testing.T) {
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
	app := &appRuntimeConfig{remote: remote}

	updated := strings.Replace(string(raw), `"site_connect_src": []`, `"site_connect_src": ["https://feedback.example.com"]`, 1)
	if _, err := remote.ApplyJSON([]byte(updated)); err != nil {
		t.Fatalf("apply updated runtime config: %v", err)
	}
	got := app.siteConnectSrc()
	if len(got) != 1 || got[0] != "https://feedback.example.com" {
		t.Fatalf("siteConnectSrc = %#v", got)
	}
}

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
	  "fingerprints": {"0e70a766342698a9": ["hermes", "opencode"]}
}`))
	if err != nil {
		t.Fatalf("apply initial Runtime providers: %v", err)
	}
	app := &appRuntimeConfig{remote: remote}
	initial := app.fce2b()
	if got := initial.RuntimeProviderFingerprints["0e70a766342698a9"]; len(got) != 2 || got[1] != "opencode" {
		t.Fatalf("initial providers = %#v", got)
	}

	initial.RuntimeProviderFingerprints["0e70a766342698a9"][0] = "mutated"
	if got := remote.RuntimeProviders().Fingerprints["0e70a766342698a9"][0]; got != "hermes" {
		t.Fatalf("caller mutated service snapshot: %q", got)
	}

	second, err := remote.ApplyRuntimeProvidersJSON([]byte(`{
	  "version": 1,
	  "fingerprints": {"ac20d08b3a999731": ["pi"]}
}`))
	if err != nil {
		t.Fatalf("apply updated Runtime providers: %v", err)
	}
	if second.Generation != first.Generation+1 {
		t.Fatalf("generation = %d, want %d", second.Generation, first.Generation+1)
	}
	updated := app.fce2b()
	if got := updated.RuntimeProviderFingerprints["ac20d08b3a999731"]; len(got) != 1 || got[0] != "pi" {
		t.Fatalf("updated providers = %#v", got)
	}
}
