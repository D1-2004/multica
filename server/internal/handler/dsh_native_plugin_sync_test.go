package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/dshprofile"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestNativePluginSnapshotDetectsInstallRemovalUpgradeAndSettings(t *testing.T) {
	base := dshprofile.Descriptor{Plugins: []dshprofile.Plugin{{PackageName: "market", Version: "1.0.0"}}}
	snapshot := dshprofile.NativeSnapshot{Plugins: []dshprofile.NativePlugin{{PackageName: "market", Version: "1.0.0"}}}
	if nativeSnapshotChanged(snapshot, base) {
		t.Fatal("unchanged native profile must not republish")
	}
	snapshot.Plugins = append(snapshot.Plugins, dshprofile.NativePlugin{PackageName: "new", Version: "1.0.0"})
	if !nativeSnapshotChanged(snapshot, base) {
		t.Fatal("installation missed")
	}
	snapshot.Plugins = nil
	if !nativeSnapshotChanged(snapshot, base) {
		t.Fatal("removal missed")
	}
	snapshot.Plugins = []dshprofile.NativePlugin{{PackageName: "market", Version: "2.0.0"}}
	if !nativeSnapshotChanged(snapshot, base) {
		t.Fatal("upgrade missed")
	}
	snapshot.Plugins[0].Version = "1.0.0"
	snapshot.Plugins[0].Patches = []dshprofile.RowOverride{{ID: "market", Config: json.RawMessage(`{"mode":"new"}`)}}
	if !nativeSnapshotChanged(snapshot, base) {
		t.Fatal("settings missed")
	}
}

func TestNativePluginConfigurationSurvivesWorkbenchEdit(t *testing.T) {
	row := db.ListDshPluginsForAgentRow{PackageName: "market", BundleRows: []byte(`["host","client"]`), ConfigOverride: []byte(`{"row_id":"host","config":{"mode":"native"},"rows":[{"id":"host","config":{"mode":"native"}},{"id":"client","config":{"theme":"dark"}}],"package":{"version":"2.0.0","integrity":"sha256-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","source_kind":"npm","source_spec":"market@2.0.0","artifact_key":"private-object","bundle_rows":["host","client"]}}`)}
	value, err := decodeAgentDshPluginOverride([]byte(`{"row_id":"host","config":{"mode":"workbench"}}`), row)
	if err != nil {
		t.Fatal(err)
	}
	if value.Package == nil || value.Package.Version != "2.0.0" || len(value.Rows) != 1 || value.Rows[0].ID != "client" {
		t.Fatal("native provenance or other row settings were lost")
	}
	if _, err := decodeAgentDshPluginOverride([]byte(`{"row_id":"host","config":{},"package":{"artifact_key":"another-employee"}}`), row); err == nil {
		t.Fatal("browser forged package provenance")
	}
	row.ConfigOverride = mustNativeJSON(t, value)
	effective, err := effectiveAgentDshPluginConfig(row)
	if err != nil || effective.Config["mode"] != "workbench" || !strings.Contains(string(effective.Rows[0].Config), "dark") {
		t.Fatal("effective native configuration mismatch", err)
	}
}

func mustNativeJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestNativePluginRowMergePreservesSettingsOnToggle(t *testing.T) {
	off := true
	rows := mergeNativePluginRows([]dshprofile.RowOverride{{ID: "host", Config: json.RawMessage(`{"token":"synthetic"}`)}}, []dshprofile.RowOverride{{ID: "host", Disabled: &off}})
	if len(rows) != 1 || rows[0].Disabled == nil || !*rows[0].Disabled || len(rows[0].Config) == 0 {
		t.Fatal("toggle discarded settings")
	}
	if dshprofile.ValidateRowOverrides(rows, []string{"other"}) == nil {
		t.Fatal("cross-plugin row accepted")
	}
}

func TestNativePluginToggleAndResetPreservePackage(t *testing.T) {
	raw := []byte(`{"row_id":"host","config":{"mode":"native"},"rows":[{"id":"host","disabled":true,"config":{"mode":"native"}}],"package":{"version":"2.0.0","bundle_rows":["host"]}}`)
	cleared, err := resetNativePluginEnablement(raw)
	if err != nil {
		t.Fatal(err)
	}
	var value AgentDshPluginConfig
	if err = json.Unmarshal(cleared, &value); err != nil || len(value.Rows) != 1 || value.Rows[0].Disabled != nil || value.Package.Version != "2.0.0" {
		t.Fatal("toggle lost version or settings", err)
	}
	reset, err := decodeAgentDshPluginOverride([]byte("null"), db.ListDshPluginsForAgentRow{ConfigOverride: raw})
	if err != nil || reset == nil || reset.Package.Version != "2.0.0" || len(reset.Config) > 0 || len(reset.Rows) > 0 {
		t.Fatal("reset downgraded native package", err)
	}
}
