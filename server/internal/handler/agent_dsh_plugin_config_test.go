package handler

import (
	"encoding/json"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"strings"
	"testing"
)

func TestAgentDshPluginConfigIsolation(t *testing.T) {
	base := db.ListDshPluginsForAgentRow{PackageName: "plugin", BundleRows: []byte(`["row"]`), ConfigRow: "row", Config: []byte(`{"identity":"workspace","defaultOnly":true}`)}
	a, b := base, base
	a.ConfigOverride = []byte(`{"row_id":"row","config":{"identity":"employee-a"}}`)
	b.ConfigOverride = []byte(`{"row_id":"row","config":{"identity":"employee-b"}}`)
	for _, tc := range []struct {
		row      db.ListDshPluginsForAgentRow
		identity string
	}{{a, "employee-a"}, {b, "employee-b"}, {base, "workspace"}} {
		result, err := effectiveAgentDshPluginConfig(tc.row)
		if err != nil {
			t.Fatal(err)
		}
		if result.Config["identity"] != tc.identity {
			t.Fatal("identity crossed employee boundary")
		}
		if tc.identity != "workspace" && result.Config["defaultOnly"] != nil {
			t.Fatal("override merged workspace identity defaults")
		}
	}
	a.ConfigOverride = []byte(`{"row_id":"row","config":{}}`)
	empty, err := effectiveAgentDshPluginConfig(a)
	if err != nil || len(empty.Config) != 0 {
		t.Fatal("empty private override must select bundle defaults, not workspace config")
	}
}

func TestAgentDshPluginConfigRejectsInvalidInputWithoutValues(t *testing.T) {
	row := db.ListDshPluginsForAgentRow{PackageName: "plugin", BundleRows: []byte(`["row","second"]`)}
	for _, raw := range []string{`[]`, `{}`, `{"config":null}`, `{"config":"secret-value"}`, `{"row_id":"secret-value","config":{}}`, `{"config":{"token":"secret-value"}}`, `{"row_id":"row","config":{},"secret-value":true}`, `{"config":{}} {}`, strings.Repeat(" ", 60001) + `{}`} {
		t.Run(raw[:min(len(raw), 40)], func(t *testing.T) {
			_, err := decodeAgentDshPluginOverride([]byte(raw), row)
			if err == nil {
				t.Fatal("invalid override accepted")
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatal("validation error exposed input values")
			}
		})
	}
	for _, raw := range []string{"", "null", " null "} {
		v, err := decodeAgentDshPluginOverride([]byte(raw), row)
		if err != nil || v != nil {
			t.Fatal("inheritance rejected")
		}
	}
}

func TestAgentDshPluginConfigFailsClosedAfterPackageChange(t *testing.T) {
	row := db.ListDshPluginsForAgentRow{PackageName: "plugin", BundleRows: []byte(`["replacement"]`), ConfigOverride: []byte(`{"row_id":"old","config":{"token":"secret-value"}}`)}
	if _, err := effectiveAgentDshPluginConfig(row); err == nil {
		t.Fatal("removed loader row was silently ignored")
	}
	if _, err := storedAgentDshPluginConfig(row); err != nil {
		t.Fatal("owner cannot read obsolete row to repair it")
	}
	row.ConfigOverride = nil
	row.Config = []byte(`[]`)
	if _, err := effectiveAgentDshPluginConfig(row); err == nil {
		t.Fatal("malformed stored config silently dropped plugin")
	}
}

func TestAgentDshPluginConfigSelectsDeclaredRow(t *testing.T) {
	row := db.ListDshPluginsForAgentRow{PackageName: "plugin", BundleRows: []byte(`["only-row"]`)}
	v, err := decodeAgentDshPluginOverride([]byte(`{"config":{"enabled":true}}`), row)
	if err != nil || v.RowID != "only-row" {
		t.Fatal("unambiguous loader row not selected")
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	row.ConfigOverride = raw
	if _, err := effectiveAgentDshPluginConfig(row); err != nil {
		t.Fatal(err)
	}
}
