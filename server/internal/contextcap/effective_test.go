package contextcap

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestMergeContextNearestLayerWins(t *testing.T) {
	prompt := func(name string, order int, text string) PromptComponent {
		return PromptComponent{Name: name, Order: order, Text: text}
	}
	// Passed out of order on purpose: MergeContext orders by layer.
	merged := MergeContext(
		ContextLayer{Layer: LayerPerson, Prompts: []PromptComponent{prompt("tone", 0, "person tone")},
			ConnectorIDs: []string{"c-org", "c-person"}, SkillIDs: []string{"s-person"},
			MCPConfig: json.RawMessage(`{"mcpServers":{"docs":{"url":"https://person"}}}`)},
		ContextLayer{Layer: LayerGlobal, ConnectorIDs: []string{"c-global"}, SkillIDs: []string{"s-global", "s-person"},
			MCPConfig: json.RawMessage(`{"mcpServers":{"docs":{"url":"https://global"},"base":{"command":"x"}},"other":1}`)},
		ContextLayer{Layer: LayerOrg, Prompts: []PromptComponent{prompt("tone", 0, "org tone"), prompt("rules", -1, "org rules")},
			ConnectorIDs: []string{"c-org", "c-global"}, MCPConfig: json.RawMessage(`{"mcpServers":{"docs":{"url":"https://org"},"crm":{"url":"https://crm"}}}`)},
		ContextLayer{Layer: LayerScene, Prompts: []PromptComponent{prompt("rules", -1, "scene rules"), prompt("group", 5, "group only")},
			ConnectorIDs: []string{"c-org"}},
	)

	type promptView struct{ Name, Text, Layer, OverriddenBy string }
	var prompts []promptView
	for _, p := range merged.Prompts {
		prompts = append(prompts, promptView{p.Name, p.Text, p.Layer, p.OverriddenBy})
	}
	wantPrompts := []promptView{
		{"rules", "org rules", LayerOrg, LayerScene},
		{"rules", "scene rules", LayerScene, ""},
		{"tone", "org tone", LayerOrg, LayerPerson},
		{"tone", "person tone", LayerPerson, ""},
		{"group", "group only", LayerScene, ""},
	}
	if !reflect.DeepEqual(prompts, wantPrompts) {
		t.Fatalf("prompts=%+v\nwant %+v", prompts, wantPrompts)
	}
	applied := merged.AppliedPrompts()
	if len(applied) != 3 || applied[0].Text != "scene rules" || applied[1].Text != "person tone" || applied[2].Text != "group only" {
		t.Fatalf("applied=%+v", applied)
	}
	block := merged.PromptBlock()
	if !strings.HasPrefix(block, ContextPromptHeading+"\n\n### rules\n\nscene rules") || strings.Contains(block, "org tone") {
		t.Fatalf("block=%q", block)
	}

	wantConnectors := []EffectiveResource{{"c-global", LayerGlobal}, {"c-org", LayerPerson}, {"c-person", LayerPerson}}
	if !reflect.DeepEqual(merged.Connectors, wantConnectors) {
		t.Fatalf("connectors=%+v", merged.Connectors)
	}
	wantSkills := []EffectiveResource{{"s-global", LayerGlobal}, {"s-person", LayerGlobal}}
	if !reflect.DeepEqual(merged.Skills, wantSkills) {
		t.Fatalf("skills=%+v", merged.Skills)
	}

	type serverView struct{ Name, Layer, OverriddenBy string }
	var servers []serverView
	for _, s := range merged.MCPServers {
		servers = append(servers, serverView{s.Name, s.Layer, s.OverriddenBy})
	}
	wantServers := []serverView{
		{"base", LayerGlobal, ""},
		{"crm", LayerOrg, ""},
		{"docs", LayerGlobal, LayerPerson},
		{"docs", LayerOrg, LayerPerson},
		{"docs", LayerPerson, ""},
	}
	if !reflect.DeepEqual(servers, wantServers) {
		t.Fatalf("servers=%+v", servers)
	}

	out, err := MergeMCPConfig(json.RawMessage(`{"mcpServers":{"docs":{"url":"https://global"},"base":{"command":"x"}},"other":1}`), merged)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
		Other      int                       `json:"other"`
	}
	if err := json.Unmarshal(out, &document); err != nil {
		t.Fatal(err)
	}
	if document.Other != 1 || len(document.MCPServers) != 3 || document.MCPServers["docs"]["url"] != "https://person" ||
		document.MCPServers["crm"]["url"] != "https://crm" || document.MCPServers["base"]["command"] != "x" {
		t.Fatalf("merged mcp config=%s", out)
	}
}

func TestMergeContextEdgeCases(t *testing.T) {
	empty := MergeContext()
	if len(empty.Prompts) != 0 || len(empty.Connectors) != 0 || len(empty.MCPServers) != 0 || empty.PromptBlock() != "" {
		t.Fatalf("empty=%+v", empty)
	}
	// A malformed layer config contributes no servers and is reported.
	merged := MergeContext(
		ContextLayer{Layer: LayerGlobal, MCPConfig: json.RawMessage(`{"mcpServers":{"a":{}}}`)},
		ContextLayer{Layer: LayerOrg, MCPConfig: json.RawMessage(`{"mcpServers":[1]}`)},
	)
	if len(merged.MCPServers) != 1 || !reflect.DeepEqual(merged.InvalidMCPLayers, []string{LayerOrg}) {
		t.Fatalf("merged=%+v", merged)
	}
	// Without scope servers the base config is returned byte for byte.
	base := json.RawMessage(`{ "mcpServers": {"a": {}} }`)
	if out, err := MergeMCPConfig(base, merged); err != nil || string(out) != string(base) {
		t.Fatalf("unchanged base=%s err=%v", out, err)
	}
	if out, err := MergeMCPConfig(nil, MergeContext()); err != nil || out != nil {
		t.Fatalf("nil base=%s err=%v", out, err)
	}
	scoped := MergeContext(ContextLayer{Layer: LayerScene, MCPConfig: json.RawMessage(`{"mcpServers":{"s":{"url":"u"}}}`)})
	if out, err := MergeMCPConfig(nil, scoped); err != nil || string(out) != `{"mcpServers":{"s":{"url":"u"}}}` {
		t.Fatalf("scope only=%s err=%v", out, err)
	}
	if out, err := MergeMCPConfig(json.RawMessage(`[1]`), scoped); err == nil || string(out) != `[1]` {
		t.Fatalf("malformed base=%s err=%v", out, err)
	}
	if _, err := MCPServers(json.RawMessage(`"x"`)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("string config: %v", err)
	}
}

func TestNormalizePromptComponents(t *testing.T) {
	out, err := NormalizePromptComponents([]PromptComponentInput{{Name: " 语气 ", Order: 2, Text: "  简短回答。 "}})
	if err != nil || len(out) != 1 || out[0].Name != "语气" || out[0].Text != "简短回答。" || out[0].Order != 2 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if _, err := NormalizePromptComponents([]PromptComponentInput{{Name: "a", Text: "x"}, {Name: " a", Text: "y"}}); !errors.Is(err, ErrDuplicatePromptName) {
		t.Fatalf("duplicate: %v", err)
	}
	tooMany := make([]PromptComponentInput, MaxPromptComponents+1)
	for i := range tooMany {
		tooMany[i] = PromptComponentInput{Name: strings.Repeat("n", i+1), Text: "t"}
	}
	for name, in := range map[string][]PromptComponentInput{
		"too many":    tooMany,
		"empty name":  {{Name: " ", Text: "t"}},
		"long name":   {{Name: strings.Repeat("名", MaxPromptComponentName+1), Text: "t"}},
		"empty text":  {{Name: "a", Text: "  "}},
		"long text":   {{Name: "a", Text: strings.Repeat("a", MaxScenePrompt+1)}},
		"nul text":    {{Name: "a", Text: "a\x00b"}},
		"ctl name":    {{Name: "a\nb", Text: "t"}},
		"order range": {{Name: "a", Text: "t", Order: 2_000_000}},
	} {
		if _, err := NormalizePromptComponents(in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
	if _, err := ListPromptComponents(context.Background(), nil, "", "", ScopeOrg, "org-a", "org-b"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("org scope key must equal the org: %v", err)
	}
}

func TestPromptComponentsAndLayersStore(t *testing.T) {
	f := openStoreTx(t)
	tenantMigrated(t, f)
	ctx := context.Background()
	actor := f.bindIdentity(t, "org-home", "")
	write := PromptComponentsWrite{WorkspaceID: f.workspaceID, AgentID: f.agentID, ScopeType: ScopeOrg, OrgID: "org-home", ScopeKey: "org-home",
		Components: []PromptComponentInput{{Name: "b", Order: 1, Text: "second"}, {Name: "a", Order: 0, Text: "first"}}, ActorID: actor}
	stored, err := ReplacePromptComponents(ctx, f.tx, write)
	if err != nil || len(stored) != 2 || stored[0].Name != "a" || stored[1].Name != "b" || stored[0].UpdatedByName != "Tenant Admin" {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	// Replacing keeps an unchanged component's id and stamp, updates a
	// changed one and drops a missing one.
	write.Components = []PromptComponentInput{{Name: "a", Order: 0, Text: "first"}, {Name: "c", Order: 2, Text: "third"}}
	write.ActorID = ""
	again, err := ReplacePromptComponents(ctx, f.tx, write)
	if err != nil || len(again) != 2 || again[0].ID != stored[0].ID || again[0].UpdatedBy != actor || again[1].Name != "c" || again[1].UpdatedBy != "" {
		t.Fatalf("again=%+v err=%v", again, err)
	}
	write.Components = []PromptComponentInput{{Name: "a", Order: 0, Text: "x"}, {Name: "a", Order: 1, Text: "y"}}
	if _, err := ReplacePromptComponents(ctx, f.tx, write); !errors.Is(err, ErrDuplicatePromptName) {
		t.Fatalf("duplicate names: %v", err)
	}
	write.Components = nil
	if cleared, err := ReplacePromptComponents(ctx, f.tx, write); err != nil || len(cleared) != 0 {
		t.Fatalf("cleared=%+v err=%v", cleared, err)
	}

	// LoadLayers reads org, scene and person layers, offered bindings only.
	if err := ReplaceOffers(ctx, f.tx, f.workspaceID, f.agentID, []string{f.connectorID}, []string{f.skillID}, actor); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []struct{ scopeType, key, resourceType, resourceID string }{
		{ScopeOrg, "org-home", ResourceConnector, f.connectorID},
		{ScopeScene, "cidLayers", ResourceSkill, f.skillID},
		{ScopePerson, "staff-1", ResourceConnector, f.connectorID},
	} {
		if _, err := UpsertBinding(ctx, f.tx, BindingWrite{WorkspaceID: f.workspaceID, AgentID: f.agentID, ScopeType: scope.scopeType, OrgID: "org-home",
			ScopeKey: scope.key, ResourceType: scope.resourceType, ResourceID: scope.resourceID, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := ReplacePromptComponents(ctx, f.tx, PromptComponentsWrite{WorkspaceID: f.workspaceID, AgentID: f.agentID, ScopeType: scope.scopeType,
			OrgID: "org-home", ScopeKey: scope.key, Components: []PromptComponentInput{{Name: "tone", Text: scope.scopeType + " tone"}}}); err != nil {
			t.Fatal(err)
		}
	}
	// A binding of a resource that is no longer offered is ignored.
	if _, err := f.tx.Exec(ctx, `INSERT INTO context_capability_binding (workspace_id, agent_id, scope_type, org_id, scope_key, resource_type, resource_id, enabled)
		VALUES ($1::uuid, $2::uuid, 'org', 'org-home', 'org-home', 'connector', $3::uuid, true)`, f.workspaceID, f.agentID, f.otherConn); err != nil {
		t.Fatal(err)
	}
	if _, err := PutScopeMCPConfig(ctx, f.tx, ScopeMCPConfigWrite{WorkspaceID: f.workspaceID, AgentID: f.agentID, ScopeType: ScopeOrg, OrgID: "org-home",
		ScopeKey: "org-home", MCPConfig: json.RawMessage(`{"mcpServers":{"crm":{"url":"https://crm"}}}`)}); err != nil {
		t.Fatal(err)
	}
	layers, err := LoadLayers(ctx, f.tx, f.workspaceID, f.agentID, LayerSelection{OrgID: "org-home", Org: true, SceneKey: "cidLayers", PersonKey: "staff-1"})
	if err != nil || len(layers) != 3 {
		t.Fatalf("layers=%+v err=%v", layers, err)
	}
	if layers[0].Layer != LayerOrg || !reflect.DeepEqual(layers[0].ConnectorIDs, []string{f.connectorID}) || len(layers[0].MCPConfig) == 0 ||
		layers[1].Layer != LayerScene || !reflect.DeepEqual(layers[1].SkillIDs, []string{f.skillID}) || layers[2].Layer != LayerPerson {
		t.Fatalf("layers=%+v", layers)
	}
	global, err := LoadGlobalLayer(ctx, f.tx, f.workspaceID, f.agentID)
	if err != nil || global.Layer != LayerGlobal {
		t.Fatalf("global=%+v err=%v", global, err)
	}
	merged := MergeContext(append([]ContextLayer{global}, layers...)...)
	if applied := merged.AppliedPrompts(); len(applied) != 1 || applied[0].Text != "person tone" {
		t.Fatalf("applied=%+v", applied)
	}
	if len(merged.Connectors) != 1 || merged.Connectors[0].Layer != LayerPerson || len(merged.Skills) != 1 || merged.Skills[0].Layer != LayerScene {
		t.Fatalf("merged=%+v", merged)
	}
	// Credentials of the selected layers, person first.
	for _, scope := range []struct{ scopeType, key string }{{ScopeOrg, "org-home"}, {ScopeScene, "cidLayers"}, {ScopePerson, "staff-1"}, {ScopePerson, "staff-2"}} {
		if _, err := UpsertCredential(ctx, f.tx, CredentialBinding{WorkspaceID: f.workspaceID, AgentID: f.agentID, ConnectorID: f.connectorID,
			ScopeType: scope.scopeType, OrgID: "org-home", ScopeKey: scope.key}, []byte{1}, scope.scopeType, ""); err != nil {
			t.Fatal(err)
		}
	}
	credentials, err := LayerCredentials(ctx, f.tx, f.workspaceID, f.agentID, LayerSelection{OrgID: "org-home", Org: true, SceneKey: "cidLayers", PersonKey: "staff-1"})
	if err != nil || len(credentials) != 3 || credentials[0].ScopeType != ScopePerson || credentials[0].ScopeKey != "staff-1" ||
		credentials[1].ScopeType != ScopeScene || credentials[2].ScopeType != ScopeOrg || len(credentials[2].Ciphertext) == 0 {
		t.Fatalf("layer credentials=%+v err=%v", credentials, err)
	}
	if credentials, err := LayerCredentials(ctx, f.tx, f.workspaceID, f.agentID, LayerSelection{OrgID: "org-home", SceneKey: "cidLayers"}); err != nil ||
		len(credentials) != 1 || credentials[0].ScopeType != ScopeScene {
		t.Fatalf("scene-only credentials=%+v err=%v", credentials, err)
	}
	if none, err := LoadLayers(ctx, f.tx, f.workspaceID, f.agentID, LayerSelection{OrgID: "org-home"}); err != nil || len(none) != 0 {
		t.Fatalf("empty selection=%+v err=%v", none, err)
	}
	if _, err := LoadLayers(ctx, f.tx, f.workspaceID, f.agentID, LayerSelection{OrgID: "org-home", SceneKey: "not-a-cid"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad scene key: %v", err)
	}
}
