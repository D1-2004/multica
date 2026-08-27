package runtimeconfig

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func validRuntimeProvidersJSON() string {
	return `{"version":1,"providers":["hermes","opencode","pi"]}`
}

func TestDocumentedRuntimeProvidersExampleMatchesSchema(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/runtime-manifest-fingerprints.example.json")
	if err != nil {
		t.Fatalf("read documented Runtime providers: %v", err)
	}
	if _, err := ParseRuntimeProviders(raw); err != nil {
		t.Fatalf("documented Runtime providers: %v", err)
	}
}

func TestParseRuntimeProvidersUsesAuthoredListWithoutCardinalityContract(t *testing.T) {
	for _, providers := range [][]string{
		{},
		{"hermes"},
		{"hermes", "opencode", "pi", "dsh", "opencode-v2", "claude", "codex"},
	} {
		encoded, err := json.Marshal(RuntimeProvidersConfig{Version: 1, Providers: providers})
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := ParseRuntimeProviders(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Providers) != len(providers) {
			t.Fatalf("providers = %#v", cfg.Providers)
		}
	}
}

func TestParseRuntimeProvidersRejectsOnlyMalformedProviderData(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown field": `{"version":1,"providers":[],"extra":true}`,
		"wrong version": `{"version":2,"providers":[]}`,
		"missing list":  `{"version":1}`,
		"bad provider":  `{"version":1,"providers":["Hermes"]}`,
		"duplicate":     `{"version":1,"providers":["hermes","hermes"]}`,
		"trailing":      validRuntimeProvidersJSON() + ` {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseRuntimeProviders([]byte(raw)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestRuntimeProvidersUpdateIsAtomicAndImmutable(t *testing.T) {
	service, err := NewStatic(mustParseConfig(t, validJSON()))
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.ApplyRuntimeProvidersJSON([]byte(validRuntimeProvidersJSON()))
	if err != nil {
		t.Fatal(err)
	}
	first.Providers[0] = "mutated"
	current := service.RuntimeProviders()
	if current.Generation != 1 || current.Providers[0] != "hermes" {
		t.Fatalf("service snapshot was mutated: %#v", current)
	}
	retained, err := service.ApplyRuntimeProvidersJSON([]byte(`{"version":1}`))
	if err == nil || retained.Generation != current.Generation {
		t.Fatalf("invalid update replaced snapshot: %#v, %v", retained, err)
	}
}

func TestRuntimeProviderLogsContainOnlySnapshotMetadata(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	logRuntimeProvidersUpdate(logger, "updated", RuntimeProvidersSnapshot{
		Version:    1,
		Providers:  []string{"secret-provider"},
		Generation: 7,
		SHA256:     "safe-hash",
	})
	logged := output.String()
	for _, forbidden := range []string{"secret-provider", `"providers"`} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("log leaked config content %q: %s", forbidden, logged)
		}
	}
	for _, required := range []string{RuntimeProvidersDiamondDataID, `"generation":7`, `"sha256":"safe-hash"`, `"count":1`} {
		if !strings.Contains(logged, required) {
			t.Fatalf("log missing %q: %s", required, logged)
		}
	}
}
