package runtimeconfig

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
)

const testRuntimeCommit = "a2eb67817f146ef4a2eb67817f146ef4a2eb6781"

func validRuntimeProvidersJSON() string {
	return `{"version":1,"fc_templates":{"tpl-1":{"runtime_commit":"` + testRuntimeCommit + `","providers":["hermes","opencode","pi"]}},"asb_commits":{"` + testRuntimeCommit + `":["hermes"]}}`
}

func TestDocumentedRuntimeProvidersExampleMatchesSchema(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/runtime-providers.example.json")
	if err != nil {
		t.Fatalf("read documented Runtime providers: %v", err)
	}
	if _, err := ParseRuntimeProviders(raw); err != nil {
		t.Fatalf("documented Runtime providers: %v", err)
	}
}

func TestParseRuntimeProvidersUsesAuthoredListsWithoutCardinalityContract(t *testing.T) {
	cfg, err := ParseRuntimeProviders([]byte(`{
  "version": 1,
  "fc_templates": {
    "tpl-empty": {"runtime_commit":"0000000000000000000000000000000000000000","providers":[]},
    "tpl-one": {"runtime_commit":"1111111111111111111111111111111111111111","providers":["hermes"]},
    "tpl-seven": {"runtime_commit":"2222222222222222222222222222222222222222","providers":["hermes","opencode","pi","dsh","opencode-v2","claude","codex"]}
  },
  "asb_commits": {}
}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.FCTemplates) != 3 || len(cfg.FCTemplates["tpl-seven"].Providers) != 7 {
		t.Fatalf("FC templates = %#v", cfg.FCTemplates)
	}
}

func TestParseRuntimeProvidersRejectsOnlyMalformedLookupData(t *testing.T) {
	for name, raw := range map[string]string{
		"unknown field":  `{"version":1,"fc_templates":{},"asb_commits":{},"extra":true}`,
		"wrong version":  `{"version":2,"fc_templates":{},"asb_commits":{}}`,
		"missing maps":   `{"version":1}`,
		"empty template": `{"version":1,"fc_templates":{"":{"runtime_commit":"0000000000000000000000000000000000000000","providers":[]}},"asb_commits":{}}`,
		"bad FC commit":  `{"version":1,"fc_templates":{"tpl":{"runtime_commit":"BAD","providers":[]}},"asb_commits":{}}`,
		"bad ASB commit": `{"version":1,"fc_templates":{},"asb_commits":{"BAD":[]}}`,
		"bad provider":   `{"version":1,"fc_templates":{"tpl":{"runtime_commit":"0000000000000000000000000000000000000000","providers":["Hermes"]}},"asb_commits":{}}`,
		"duplicate":      `{"version":1,"fc_templates":{},"asb_commits":{"0000000000000000000000000000000000000000":["hermes","hermes"]}}`,
		"trailing":       validRuntimeProvidersJSON() + ` {}`,
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
	entry := first.FCTemplates["tpl-1"]
	entry.Providers[0] = "mutated"
	first.FCTemplates["tpl-1"] = entry
	current := service.RuntimeProviders()
	if current.Generation != 1 || current.FCTemplates["tpl-1"].Providers[0] != "hermes" {
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
		Version: 1,
		FCTemplates: map[string]FCTemplateProviders{
			"secret-template": {RuntimeCommit: testRuntimeCommit, Providers: []string{"secret-provider"}},
		},
		Generation: 7,
		SHA256:     "safe-hash",
	})
	logged := output.String()
	for _, forbidden := range []string{"secret-template", "secret-provider", testRuntimeCommit, `"fc_templates"`} {
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
