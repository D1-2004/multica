package main

import (
	"os"
	"testing"

	"github.com/multica-ai/multica/server/pkg/runtimeconfig"
)

func TestAppRuntimeConfigReadsCurrentManifestFingerprintSnapshot(t *testing.T) {
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

	first, err := remote.ApplyManifestFingerprintsJSON([]byte(`{
  "version": 1,
  "fingerprints": {
    "0e70a766342698a9": {
      "hermes": "0.19.0",
      "opencode": "v1.18.19",
      "opencode-v2": "0.0.0-beta-202608110357",
      "dsh": "0.1.0-rc.8",
      "pi": "0.84.2",
      "dws": "v1.0.59"
    }
  }
}`))
	if err != nil {
		t.Fatalf("apply initial manifest fingerprints: %v", err)
	}
	app := &appRuntimeConfig{remote: remote}
	initial := app.fce2b()
	if got := initial.ManifestV7ComponentVersions["0e70a766342698a9"]["dws"]; got != "v1.0.59" {
		t.Fatalf("initial DWS version = %q", got)
	}

	initial.ManifestV7ComponentVersions["0e70a766342698a9"]["dws"] = "mutated"
	if got := remote.ManifestFingerprints().Fingerprints["0e70a766342698a9"]["dws"]; got != "v1.0.59" {
		t.Fatalf("caller mutated service snapshot: %q", got)
	}

	second, err := remote.ApplyManifestFingerprintsJSON([]byte(`{
  "version": 1,
  "fingerprints": {
    "ac20d08b3a999731": {
      "hermes": "0.19.0",
      "opencode": "v1.18.19",
      "opencode-v2": "0.0.0-beta-202608110357",
      "dsh": "0.1.0-rc.8",
      "pi": "0.84.2",
      "dws": "v1.0.60-beta.1"
    }
  }
}`))
	if err != nil {
		t.Fatalf("apply updated manifest fingerprints: %v", err)
	}
	if second.Generation != first.Generation+1 {
		t.Fatalf("generation = %d, want %d", second.Generation, first.Generation+1)
	}
	updated := app.fce2b()
	if got := updated.ManifestV7ComponentVersions["ac20d08b3a999731"]["dws"]; got != "v1.0.60-beta.1" {
		t.Fatalf("updated DWS version = %q", got)
	}
	if _, ok := updated.ManifestV7ComponentVersions["0e70a766342698a9"]; ok {
		t.Fatal("app runtime config retained removed fingerprint")
	}
}
