package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/agentsource"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestPackageDshPluginExportAndRebind(t *testing.T) {
	row := db.ListDshPluginsForAgentRow{ID: parseUUID("11111111-1111-4111-8111-111111111111"), PackageName: "dsh-fixture", ResolvedVersion: "1.2.3", Integrity: "sha256-" + strings.Repeat("a", 64), BundleRows: []byte(`["fixture-row"]`), Config: []byte(`{"token":"workspace-secret"}`), ConfigOverride: []byte(`{"row_id":"fixture-row","config":{"token":"employee-secret","enabled":true}}`), Enabled: false}
	notes := &agentExportNotes{}
	exported, err := exportPackageDshPlugin(notes, row)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(exported)
	if strings.Contains(string(raw), "employee-secret") || strings.Contains(string(raw), "workspace-secret") || strings.Contains(string(raw), uuidToString(row.ID)) {
		t.Fatal("export leaked private configuration or workspace identity")
	}
	other := row
	other.ID = parseUUID("22222222-2222-4222-8222-222222222222")
	again, err := exportPackageDshPlugin(&agentExportNotes{}, other)
	if err != nil || again["ref"] != exported["ref"] {
		t.Fatal("artifact alias changed across workspaces")
	}
	manifest := map[string]any{"$schema": "agent.schema.json", "version": "multica.agent/v2", "name": "Plugin recipe", "instructions": "AGENTS.md", "skills": []any{}, "configuration": map[string]any{}, "dsh_plugins": []any{exported}}
	archive, err := agentsource.ExportAgentPackage(t.Context(), manifest, "Instructions", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := agentsource.ParseAgentPackage(t.Context(), archive)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := pkg.Bundle()
	if err != nil {
		t.Fatal(err)
	}
	requirements := packageRequirements(bundle)
	if len(requirements.DshPlugins) != 1 || len(requirements.Secrets) != 1 {
		t.Fatal("preview omitted a plugin or credential requirement")
	}
	req := CreateAgentPackageRequest{Secrets: map[string]string{requirements.Secrets[0]: "new-employee-secret"}, DshPluginBindings: map[string]string{requirements.DshPlugins[0].Ref: uuidToString(other.ID)}}
	definition, err := preparePackageConfiguration(&req, map[string]json.RawMessage{}, bundle)
	if err != nil {
		t.Fatal(err)
	}
	config, err := resolvePackagePluginConfig(definition.DshPlugins[0], db.DshPlugin{PackageName: row.PackageName, ResolvedVersion: row.ResolvedVersion, Integrity: row.Integrity, BundleRows: row.BundleRows, Config: []byte(`{"token":"destination-workspace-secret"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "new-employee-secret") || strings.Contains(string(config), "workspace-secret") || definition.DshPlugins[0].Enabled {
		t.Fatal("import reused another identity or changed assignment state")
	}
	var previewPlugins []map[string]any
	previewRaw, _ := json.Marshal(packageDefinitionPreview(bundle)["dsh_plugins"])
	_ = json.Unmarshal(previewRaw, &previewPlugins)
	previewConfig := previewPlugins[0]["config"].(map[string]any)
	if previewConfig["token"].(map[string]any)["secret_ref"] != requirements.Secrets[0] {
		t.Fatal("preview rewrote the secret reference")
	}
}

func TestPackageDshPluginBindingsRejectMissingDuplicateAndUnknown(t *testing.T) {
	id := "11111111-1111-4111-8111-111111111111"
	cases := []struct {
		plugins  []packageDshPlugin
		bindings map[string]string
	}{
		{[]packageDshPlugin{{PackageDshPluginRequirement: PackageDshPluginRequirement{Ref: "p"}}}, nil},
		{[]packageDshPlugin{{PackageDshPluginRequirement: PackageDshPluginRequirement{Ref: "p"}}}, map[string]string{"p": "bad"}},
		{nil, map[string]string{"unexpected": id}},
		{[]packageDshPlugin{{PackageDshPluginRequirement: PackageDshPluginRequirement{Ref: "p"}}, {PackageDshPluginRequirement: PackageDshPluginRequirement{Ref: "q"}}}, map[string]string{"p": id, "q": id}},
	}
	for _, tc := range cases {
		if err := validatePackagePluginBindings(tc.plugins, tc.bindings); err == nil {
			t.Fatal("unsafe destination mapping accepted")
		}
	}
}

func TestPackageDshPluginRejectsArtifactDriftAndInvalidRows(t *testing.T) {
	p := packageDshPlugin{PackageDshPluginRequirement: PackageDshPluginRequirement{Ref: "p", PackageName: "plugin", Version: "1", Integrity: "sha256-" + strings.Repeat("a", 64)}, RowID: "row", Config: map[string]any{"token": "fixture-secret"}}
	good := db.DshPlugin{PackageName: p.PackageName, ResolvedVersion: p.Version, Integrity: p.Integrity, BundleRows: []byte(`["row"]`)}
	for _, field := range []string{"package", "version", "integrity", "row"} {
		row := good
		switch field {
		case "package":
			row.PackageName = "another"
		case "version":
			row.ResolvedVersion = "2"
		case "integrity":
			row.Integrity = "sha256-" + strings.Repeat("b", 64)
		case "row":
			row.BundleRows = []byte(`["another"]`)
		}
		if _, err := resolvePackagePluginConfig(p, row); err == nil || strings.Contains(err.Error(), "fixture-secret") {
			t.Fatalf("%s drift accepted or error leaked a value", field)
		}
	}
}

func TestPackageDshPluginActorBoundary(t *testing.T) {
	for _, value := range []any{[]any{}, []any{map[string]any{"ref": "plugin"}}} {
		encoded, _ := json.Marshal(value)
		definition := map[string]json.RawMessage{"dsh_plugins": encoded}
		if err := validatePackageActor(definition, ""); err != nil {
			t.Fatal(err)
		}
		for _, actor := range []string{"agent", "daemon", "runtime", "future-machine"} {
			if err := validatePackageActor(definition, actor); err == nil {
				t.Fatalf("allowed %s", actor)
			}
		}
	}
	if err := validatePackageActor(map[string]json.RawMessage{"instructions": json.RawMessage(`"AGENTS.md"`)}, "agent"); err != nil {
		t.Fatal(err)
	}
}

func TestPackageDshPluginRequirementsRedactLiteralConfig(t *testing.T) {
	var definition map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"dsh_plugins":[{"ref":"lens","enabled":false,"config":{"token":"literal-private-value"}}]}`), &definition); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(packageRequirements(agentsource.Bundle{Definition: definition}))
	if err != nil || strings.Contains(string(encoded), "literal-private-value") {
		t.Fatal("requirements exposed private plugin values")
	}
}
