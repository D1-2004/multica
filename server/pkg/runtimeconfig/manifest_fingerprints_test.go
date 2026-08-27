package runtimeconfig

import (
	"bytes"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestDocumentedManifestFingerprintsExampleMatchesSchema(t *testing.T) {
	raw, err := os.ReadFile("../../../docs/runtime-manifest-fingerprints.example.json")
	if err != nil {
		t.Fatalf("read documented manifest fingerprints: %v", err)
	}
	if _, err := ParseManifestFingerprintsStrict(raw); err != nil {
		t.Fatalf("documented manifest fingerprints: %v", err)
	}
}

func validManifestFingerprintsJSON() string {
	return `{
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
}`
}

func TestParseManifestFingerprintsStrictAcceptsExactContract(t *testing.T) {
	cfg, err := ParseManifestFingerprintsStrict([]byte(validManifestFingerprintsJSON()))
	if err != nil {
		t.Fatalf("ParseManifestFingerprintsStrict: %v", err)
	}
	if cfg.Version != ManifestFingerprintsSchemaVersion || len(cfg.Fingerprints) != 1 {
		t.Fatalf("config = %#v", cfg)
	}
	if got := cfg.Fingerprints["0e70a766342698a9"]["dws"]; got != "v1.0.59" {
		t.Fatalf("DWS version = %q", got)
	}
}

func TestManifestFingerprintHashMatchesDocumentedExampleKey(t *testing.T) {
	got, err := manifestFingerprint(map[string]string{
		"hermes":      "0.19.0",
		"opencode":    "v1.18.19",
		"opencode-v2": "0.0.0-beta-202608110357",
		"dsh":         "0.1.0-rc.8",
		"pi":          "0.84.2",
		"dws":         "v1.0.59",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "0e70a766342698a9" {
		t.Fatalf("local contract hash = %q, want 0e70a766342698a9", got)
	}
}

func TestManifestProvidersForFingerprintAcceptsAnySupportedCardinality(t *testing.T) {
	components := map[string]string{
		"hermes":      "0.19.0",
		"opencode":    "v1.18.19",
		"opencode-v2": "0.0.0-beta-202608110357",
		"dsh":         "0.1.0-rc.8",
		"pi":          "0.84.2",
		"dws":         "v1.0.60-beta.2",
	}
	for _, providers := range [][]string{
		{"hermes"},
		{"hermes", "opencode", "pi", "dsh", "opencode-v2"},
		{"hermes", "opencode", "pi", "dsh", "opencode-v2", "claude", "codex"},
	} {
		fingerprint, err := manifestFingerprintForProviders(components, providers)
		if err != nil {
			t.Fatal(err)
		}
		resolved, matched, err := ManifestProvidersForFingerprint(fingerprint, components)
		if err != nil {
			t.Fatal(err)
		}
		if !matched || !reflect.DeepEqual(resolved, providers) {
			t.Fatalf("fingerprint %s resolved providers %#v, want %#v", fingerprint, resolved, providers)
		}
	}
	reversedFingerprint, err := manifestFingerprintForProviders(components, []string{"opencode", "hermes"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved, matched, err := ManifestProvidersForFingerprint(reversedFingerprint, components); err != nil || matched || resolved != nil {
		t.Fatalf("non-canonical provider order resolved=%#v matched=%v err=%v", resolved, matched, err)
	}
}

func TestManifestCapabilitiesForProvidersDoesNotAdvertiseMissingRunners(t *testing.T) {
	tests := []struct {
		providers []string
		want      []string
	}{
		{
			providers: []string{"hermes"},
			want: []string{
				"dws", "dws.im_event", "mcp", "runtime_start_events_v1", "llm_trace_v1",
				"a2a-invocation-v2", "a2a_inbound_hermes_v1",
			},
		},
		{
			providers: []string{"dsh"},
			want: []string{
				"dws", "dws.im_event", "mcp", "runtime_start_events_v1", "llm_trace_v1",
				"a2a_inbound_opencode_v1", "a2a-invocation-v2", "dsh_trajectory_v1",
			},
		},
		{
			providers: []string{"opencode-v2"},
			want: []string{
				"dws", "dws.im_event", "mcp", "runtime_start_events_v1", "llm_trace_v1",
				"a2a_inbound_opencode_v1", "a2a-invocation-v2",
			},
		},
		{
			providers: []string{"hermes", "opencode", "pi", "dsh", "opencode-v2"},
			want: []string{
				"dws", "dws.im_event", "mcp", "runtime_start_events_v1", "llm_trace_v1",
				"a2a_inbound_opencode_v1", "a2a-invocation-v2", "a2a_inbound_hermes_v1",
				"a2a_inbound_pi_v1", "dsh_trajectory_v1",
			},
		},
	}
	for _, test := range tests {
		if got := ManifestCapabilitiesForProviders(test.providers); !reflect.DeepEqual(got, test.want) {
			t.Fatalf("providers %#v capabilities = %#v, want %#v", test.providers, got, test.want)
		}
	}
}

func TestParseManifestFingerprintsStrictAcceptsFiveProviderCatalogKey(t *testing.T) {
	raw := `{
  "version": 1,
  "fingerprints": {
    "baedb216407a5060": {
      "hermes": "0.19.0",
      "opencode": "v1.18.19",
      "opencode-v2": "0.0.0-beta-202608110357",
      "dsh": "0.1.0-rc.8",
      "pi": "0.84.2",
      "dws": "v1.0.59"
    }
  }
}`
	cfg, err := ParseManifestFingerprintsStrict([]byte(raw))
	if err != nil {
		t.Fatalf("ParseManifestFingerprintsStrict: %v", err)
	}
	if got := cfg.Fingerprints["baedb216407a5060"]["dws"]; got != "v1.0.59" {
		t.Fatalf("catalog DWS version = %q", got)
	}
}

func TestParseManifestFingerprintsStrictRejectsInvalidDocuments(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "unknown field", raw: strings.Replace(validManifestFingerprintsJSON(), `"version": 1`, `"version": 1, "versoin": 1`, 1)},
		{name: "wrong version", raw: strings.Replace(validManifestFingerprintsJSON(), `"version": 1`, `"version": 2`, 1)},
		{name: "empty catalog", raw: `{"version":1,"fingerprints":{}}`},
		{name: "uppercase fingerprint", raw: strings.Replace(validManifestFingerprintsJSON(), "0e70a766342698a9", "BAEDB216407A5060", 1)},
		{name: "short fingerprint", raw: strings.Replace(validManifestFingerprintsJSON(), "0e70a766342698a9", "baedb216", 1)},
		{name: "missing component", raw: strings.Replace(validManifestFingerprintsJSON(), `      "dws": "v1.0.59"`, `      "unused": "v1.0.59"`, 1)},
		{name: "extra component", raw: strings.Replace(validManifestFingerprintsJSON(), `      "dws": "v1.0.59"`, `      "dws": "v1.0.59", "extra": "v1"`, 1)},
		{name: "blank version", raw: strings.Replace(validManifestFingerprintsJSON(), `"dws": "v1.0.59"`, `"dws": " "`, 1)},
		{name: "surrounding whitespace", raw: strings.Replace(validManifestFingerprintsJSON(), `"dws": "v1.0.59"`, `"dws": " v1.0.59"`, 1)},
		{name: "trailing value", raw: validManifestFingerprintsJSON() + ` {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseManifestFingerprintsStrict([]byte(test.raw)); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

// A structurally valid document can still carry a fingerprint that no provider
// subset supported by this binary would mint. That disagreement costs one
// unpublished FC/E2B template
// (service.applyFCE2BTemplateManifestAlias skips an alias it cannot resolve).
// It must never cost the server. A structurally broken document still must.
func TestStaleFingerprintDocumentLoadsInsteadOfKillingStartup(t *testing.T) {
	service, err := NewStatic(mustParseConfig(t, validJSON()))
	if err != nil {
		t.Fatalf("NewStatic: %v", err)
	}

	// Keep the key well-shaped but ensure it matches no canonical non-empty
	// provider subset. Five-provider keys such as baedb216407a5060 are valid and
	// have their own strict regression above.
	const authoredKey = "ffffffffffffffff"
	stale := strings.Replace(validManifestFingerprintsJSON(), "0e70a766342698a9", authoredKey, 1)
	components := map[string]string{
		"hermes": "0.19.0", "opencode": "v1.18.19",
		"opencode-v2": "0.0.0-beta-202608110357", "dsh": "0.1.0-rc.8",
		"pi": "0.84.2", "dws": "v1.0.59",
	}
	if _, matched, err := ManifestProvidersForFingerprint(authoredKey, components); err != nil || matched {
		t.Fatalf("foreign fingerprint unexpectedly resolved: matched=%v err=%v", matched, err)
	}

	if _, err := ParseManifestFingerprintsStrict([]byte(stale)); err == nil {
		t.Fatal("the strict contract must still reject a disagreeing key — CI and the documented example rely on it")
	}

	snapshot, err := service.ApplyManifestFingerprintsJSON([]byte(stale))
	if err != nil {
		t.Fatalf("a disagreeing key must not fail the load that startup exits on: %v", err)
	}
	// Kept as authored, not re-keyed: which side is stale is not knowable here,
	// and inventing a key breaks lookups whenever the images are the right ones.
	if got := snapshot.Fingerprints[authoredKey]["dws"]; got != "v1.0.59" {
		t.Fatalf("entry was not stored under the key the document authored: %#v", snapshot.Fingerprints)
	}
	if _, rekeyed := snapshot.Fingerprints["0e70a766342698a9"]; rekeyed {
		t.Fatal("entry was re-keyed to this binary's computed fingerprint")
	}

	// The disagreement is still reported, and names both sides.
	mismatches, err := ManifestFingerprintsConfig{
		Version:      ManifestFingerprintsSchemaVersion,
		Fingerprints: snapshot.Fingerprints,
	}.FingerprintMismatches()
	if err != nil {
		t.Fatalf("FingerprintMismatches: %v", err)
	}
	if len(mismatches) != 1 ||
		mismatches[0].Fingerprint != authoredKey ||
		mismatches[0].Expected != "0e70a766342698a9" {
		t.Fatalf("mismatch report = %#v", mismatches)
	}

	// A document that cannot be indexed at all is still a load failure: there is
	// nothing to serve from it, so tolerating it would only hide the breakage.
	for name, raw := range map[string]string{
		"wrong version":   strings.Replace(validManifestFingerprintsJSON(), `"version": 1`, `"version": 2`, 1),
		"empty catalog":   `{"version":1,"fingerprints":{}}`,
		"bad key shape":   strings.Replace(validManifestFingerprintsJSON(), "0e70a766342698a9", "NOTHEX", 1),
		"missing key":     strings.Replace(validManifestFingerprintsJSON(), `      "dws": "v1.0.59"`, `      "unused": "v1.0.59"`, 1),
		"unknown field":   strings.Replace(validManifestFingerprintsJSON(), `"version": 1`, `"version": 1, "versoin": 1`, 1),
		"trailing value":  validManifestFingerprintsJSON() + ` {}`,
		"blank component": strings.Replace(validManifestFingerprintsJSON(), `"dws": "v1.0.59"`, `"dws": " "`, 1),
	} {
		if _, err := service.ApplyManifestFingerprintsJSON([]byte(raw)); err == nil {
			t.Errorf("%s: a structurally unusable document must still fail the load", name)
		}
	}
}

func TestManifestFingerprintsUpdateIsAtomicAndImmutable(t *testing.T) {
	service, err := NewStatic(mustParseConfig(t, validJSON()))
	if err != nil {
		t.Fatalf("NewStatic: %v", err)
	}
	first, err := service.ApplyManifestFingerprintsJSON([]byte(validManifestFingerprintsJSON()))
	if err != nil {
		t.Fatalf("ApplyManifestFingerprintsJSON: %v", err)
	}
	first.Fingerprints["0e70a766342698a9"]["dws"] = "mutated"
	current := service.ManifestFingerprints()
	if current.Generation != 1 || current.Fingerprints["0e70a766342698a9"]["dws"] != "v1.0.59" {
		t.Fatalf("service-owned snapshot was mutated: %#v", current)
	}

	retained, err := service.ApplyManifestFingerprintsJSON([]byte(`{"version":1,"fingerprints":{}}`))
	if err == nil {
		t.Fatal("expected invalid update error")
	}
	if retained.Generation != current.Generation || retained.SHA256 != current.SHA256 {
		t.Fatalf("invalid update replaced snapshot: %#v", retained)
	}

	updated := strings.Replace(validManifestFingerprintsJSON(), `"0e70a766342698a9"`, `"ac20d08b3a999731"`, 1)
	updated = strings.Replace(updated, `"dws": "v1.0.59"`, `"dws": "v1.0.60-beta.1"`, 1)
	next, err := service.ApplyManifestFingerprintsJSON([]byte(updated))
	if err != nil {
		t.Fatalf("apply valid update: %v", err)
	}
	if next.Generation != 2 || next.SHA256 == current.SHA256 || next.Fingerprints["ac20d08b3a999731"]["dws"] != "v1.0.60-beta.1" {
		t.Fatalf("updated snapshot = %#v", next)
	}
}

func TestDiamondServiceAppliesManifestFingerprintUpdates(t *testing.T) {
	client := &fakeDiamondClient{content: validJSON()}
	service, err := newDiamondService(nil, true, func() (diamondClient, error) { return client, nil })
	if err != nil {
		t.Fatalf("newDiamondService: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	first := service.ManifestFingerprints()
	if first.Generation != 1 || client.manifestFingerprintsOnChange == nil {
		t.Fatalf("initial fingerprints = %#v, listener=%v", first, client.manifestFingerprintsOnChange != nil)
	}

	updated := strings.Replace(validManifestFingerprintsJSON(), `"0e70a766342698a9"`, `"ac20d08b3a999731"`, 1)
	updated = strings.Replace(updated, `"dws": "v1.0.59"`, `"dws": "v1.0.60-beta.1"`, 1)
	client.manifestFingerprintsOnChange(updated)
	second := service.ManifestFingerprints()
	if second.Generation != 2 || second.Fingerprints["ac20d08b3a999731"]["dws"] != "v1.0.60-beta.1" {
		t.Fatalf("updated fingerprints = %#v", second)
	}

	client.manifestFingerprintsOnChange(`{"version":1,"fingerprints":{}}`)
	retained := service.ManifestFingerprints()
	if retained.Generation != second.Generation || retained.SHA256 != second.SHA256 {
		t.Fatalf("invalid listener update replaced snapshot: %#v", retained)
	}
}

func TestManifestFingerprintLogsContainOnlySnapshotMetadata(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	logManifestFingerprintsUpdate(logger, "updated", ManifestFingerprintsSnapshot{
		Version: 1,
		Fingerprints: map[string]map[string]string{
			"secret-fingerprint": {"dws": "secret-version"},
		},
		Generation: 7,
		SHA256:     "safe-hash",
	})
	logged := output.String()
	for _, forbidden := range []string{"secret-fingerprint", "secret-version", `"version"`, `"fingerprints"`} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("log leaked config content %q: %s", forbidden, logged)
		}
	}
	for _, required := range []string{ManifestFingerprintsDiamondDataID, `"generation":7`, `"sha256":"safe-hash"`, `"count":1`} {
		if !strings.Contains(logged, required) {
			t.Fatalf("log missing %q: %s", required, logged)
		}
	}
}
