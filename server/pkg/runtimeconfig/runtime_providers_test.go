package runtimeconfig

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func validRuntimeProvidersJSON() string {
	return `{"version":1,"fingerprints":{"a2eb67817f146ef4":["hermes","opencode","pi","dsh","opencode-v2","claude","codex"]}}`
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

func TestParseRuntimeProvidersUsesOneFingerprintPerProviderCombination(t *testing.T) {
	cfg, err := ParseRuntimeProviders([]byte(validRuntimeProvidersJSON()))
	if err != nil {
		t.Fatal(err)
	}
	providers := cfg.Fingerprints["a2eb67817f146ef4"]
	if len(cfg.Fingerprints) != 1 || len(providers) != 7 || providers[6] != "codex" {
		t.Fatalf("fingerprints = %#v", cfg.Fingerprints)
	}
}

func TestParseRuntimeProvidersDoesNotConstrainProviderCardinality(t *testing.T) {
	for _, raw := range []string{
		`{"version":1,"fingerprints":{}}`,
		`{"version":1,"fingerprints":{"0000000000000000":[]}}`,
		`{"version":1,"fingerprints":{"1111111111111111":["hermes"]}}`,
	} {
		if _, err := ParseRuntimeProviders([]byte(raw)); err != nil {
			t.Fatalf("parse %s: %v", raw, err)
		}
	}
}

func TestParseRuntimeProvidersRejectsMalformedLookupData(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown field":   `{"version":1,"fingerprints":{},"extra":true}`,
		"wrong version":   `{"version":2,"fingerprints":{}}`,
		"missing map":     `{"version":1}`,
		"bad fingerprint": `{"version":1,"fingerprints":{"BAD":[]}}`,
		"bad provider":    `{"version":1,"fingerprints":{"0000000000000000":["Hermes"]}}`,
		"duplicate":       `{"version":1,"fingerprints":{"0000000000000000":["hermes","hermes"]}}`,
		"trailing":        validRuntimeProvidersJSON() + ` {}`,
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
	first.Fingerprints["a2eb67817f146ef4"][0] = "mutated"
	current := service.RuntimeProviders()
	if current.Generation != 1 || current.Fingerprints["a2eb67817f146ef4"][0] != "hermes" {
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
		Version:      1,
		Fingerprints: map[string][]string{"secretfingerprin": {"secret-provider"}},
		Generation:   7,
		SHA256:       "safe-hash",
	})
	logged := output.String()
	for _, forbidden := range []string{"secretfingerprin", "secret-provider", `"fingerprints"`} {
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
