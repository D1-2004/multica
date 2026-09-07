package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

// The composed value is what the sandbox actually consumes, so its shape is a
// contract with the runtime adapter rather than an internal detail. These tests
// pin the two rules that are easy to get wrong and impossible to see fail:
// an empty config must be omitted, and a disabled binding must not appear.

func decodeEntries(t *testing.T, raw string) []dshPluginSetEntry {
	t.Helper()
	var entries []dshPluginSetEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		t.Fatalf("composed value is not valid JSON: %v\n%s", err, raw)
	}
	return entries
}

func TestDshPluginSetOmitsAnEmptyConfig(t *testing.T) {
	// A patch replaces a loader row's config wholesale. Emitting `"config": {}`
	// would therefore erase the defaults the plugin's own bundle layer supplies
	// — the plugin would load and then fail on a missing required field.
	encoded, err := json.Marshal([]dshPluginSetEntry{{
		Name:      "dsh-mcp-lens",
		Source:    "npm:dsh-mcp-lens@0.1.0-rc.9",
		Integrity: "sha256-" + "a8e4bf8389d0107379c13c845feb3c7c0c26d4aa3312391640e1fed074d39dbc",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(encoded); strings.Contains(got, "\"config\"") || strings.Contains(got, "\"row_id\"") {
		t.Fatalf("an entry with no config must omit both config and row_id, got %s", got)
	}
	entries := decodeEntries(t, string(encoded))
	if len(entries) != 1 || entries[0].Name != "dsh-mcp-lens" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestDshPluginSetCarriesConfigWithItsRow(t *testing.T) {
	// A row id routinely differs from the package name, so config must travel
	// with the row it addresses or it lands on nothing.
	encoded, err := json.Marshal([]dshPluginSetEntry{{
		Name:   "dsh-mcp-lens",
		Source: "npm:dsh-mcp-lens@0.1.0-rc.9",
		Config: map[string]any{"servers": []any{}},
		RowID:  "mcp-lens",
	}})
	if err != nil {
		t.Fatal(err)
	}
	entries := decodeEntries(t, string(encoded))
	if len(entries) != 1 {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].RowID != "mcp-lens" {
		t.Errorf("row_id = %q, want mcp-lens", entries[0].RowID)
	}
	if _, ok := entries[0].Config["servers"]; !ok {
		t.Errorf("config = %v, want it to carry servers", entries[0].Config)
	}
}

func TestDshPluginSetFieldNamesMatchTheAdapter(t *testing.T) {
	// These names are the runtime adapter's wire contract. Renaming a field
	// here silently stops every plugin from loading, with no error anywhere.
	encoded, err := json.Marshal(dshPluginSetEntry{
		Name: "p", Source: "npm:p@1", Integrity: "sha256-x", RowID: "r",
		Config: map[string]any{"a": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"name", "source", "integrity", "config", "row_id"} {
		if _, ok := decoded[field]; !ok {
			t.Errorf("the adapter expects a %q field; it is missing from %s", field, encoded)
		}
	}
	if len(decoded) != 5 {
		t.Errorf("unexpected extra fields in %s", encoded)
	}
}
