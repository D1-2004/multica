package contextcap

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// A disabled prompt component and a disabled scope MCP server take no part in
// the merge: neither applies, and neither overrides an outer one of the same
// name. The "disabled" key never reaches the runtime config.
func TestMergeContextSkipsDisabledComponents(t *testing.T) {
	merged := MergeContext(
		ContextLayer{Layer: LayerGlobal, MCPConfig: json.RawMessage(`{"mcpServers":{"docs":{"url":"https://global"},"base":{"command":"x","disabled":true}}}`)},
		ContextLayer{Layer: LayerOrg, Prompts: []PromptComponent{{Name: "tone", Text: "org tone"}, {Name: "rules", Text: "org rules"}},
			MCPConfig: json.RawMessage(`{"mcpServers":{"crm":{"url":"https://org-crm","disabled":false}}}`)},
		ContextLayer{Layer: LayerScene, Prompts: []PromptComponent{{Name: "tone", Text: "scene tone", Disabled: true}, {Name: "off", Text: "never", Disabled: true}},
			MCPConfig: json.RawMessage(`{"mcpServers":{"docs":{"url":"https://scene","disabled":true},"crm":{"url":"https://scene-crm","disabled":true}}}`)},
	)
	type promptView struct{ Name, Text, Layer, OverriddenBy string }
	var prompts []promptView
	for _, p := range merged.Prompts {
		prompts = append(prompts, promptView{p.Name, p.Text, p.Layer, p.OverriddenBy})
	}
	want := []promptView{{"rules", "org rules", LayerOrg, ""}, {"tone", "org tone", LayerOrg, ""}}
	if !reflect.DeepEqual(prompts, want) {
		t.Fatalf("prompts=%+v want %+v", prompts, want)
	}
	if block := merged.PromptBlock(); strings.Contains(block, "scene tone") || strings.Contains(block, "never") || !strings.Contains(block, "org tone") {
		t.Fatalf("block=%q", block)
	}
	type serverView struct{ Name, Layer, OverriddenBy, Config string }
	var servers []serverView
	for _, s := range merged.MCPServers {
		servers = append(servers, serverView{s.Name, s.Layer, s.OverriddenBy, string(s.Config)})
	}
	// The agent's own config is taken as it is (its "disabled" key included);
	// the org server applies without its "disabled": false.
	wantServers := []serverView{
		{"base", LayerGlobal, "", `{"command":"x","disabled":true}`},
		{"crm", LayerOrg, "", `{"url":"https://org-crm"}`},
		{"docs", LayerGlobal, "", `{"url":"https://global"}`},
	}
	if !reflect.DeepEqual(servers, wantServers) {
		t.Fatalf("servers=%+v\nwant %+v", servers, wantServers)
	}
	out, err := MergeMCPConfig(json.RawMessage(`{"mcpServers":{"docs":{"url":"https://global"}}}`), merged)
	if err != nil || strings.Contains(string(out), "disabled") || strings.Contains(string(out), "scene") || !strings.Contains(string(out), "https://org-crm") {
		t.Fatalf("runtime config=%s err=%v", out, err)
	}
}

func TestNormalizeRemoteMCPConfig(t *testing.T) {
	ok := map[string]string{
		"one remote server":   `{"mcpServers":{"docs":{"url":"https://docs.example.test/mcp"}}}`,
		"every allowed key":   `{"mcpServers":{"docs":{"type":"streamable-http","url":"http://10.0.0.1:8080/mcp","headers":{"Authorization":"Bearer x","X-Team":"a"},"disabled":true}}}`,
		"sse and http types":  `{"mcpServers":{"a":{"type":"sse","url":"https://a.test"},"b":{"type":"http","url":"https://b.test","headers":{}}}}`,
		"chinese server name": `{"mcpServers":{"文档":{"url":"https://docs.test"}}}`,
	}
	for name, raw := range ok {
		out, err := NormalizeRemoteMCPConfig(json.RawMessage(raw))
		if err != nil || out == nil {
			t.Errorf("%s: out=%s err=%v", name, out, err)
		}
	}
	for name, raw := range map[string]string{"null": `null`, "empty": `{}`, "no servers": `{"mcpServers":{}}`, "null servers": `{"mcpServers":null}`} {
		if out, err := NormalizeRemoteMCPConfig(json.RawMessage(raw)); err != nil || out != nil {
			t.Errorf("%s clears: out=%s err=%v", name, out, err)
		}
	}
	refused := map[string]string{
		"command":           `{"mcpServers":{"x":{"command":"npx","args":["-y","pkg"]}}}`,
		"url and command":   `{"mcpServers":{"x":{"url":"https://x.test","command":"sh"}}}`,
		"args":              `{"mcpServers":{"x":{"url":"https://x.test","args":["a"]}}}`,
		"env":               `{"mcpServers":{"x":{"url":"https://x.test","env":{"A":"b"}}}}`,
		"cwd":               `{"mcpServers":{"x":{"url":"https://x.test","cwd":"/tmp"}}}`,
		"unknown key":       `{"mcpServers":{"x":{"url":"https://x.test","timeout":5}}}`,
		"no url":            `{"mcpServers":{"x":{"type":"http"}}}`,
		"file url":          `{"mcpServers":{"x":{"url":"file:///etc/passwd"}}}`,
		"ws url":            `{"mcpServers":{"x":{"url":"ws://x.test"}}}`,
		"relative url":      `{"mcpServers":{"x":{"url":"/mcp"}}}`,
		"no host":           `{"mcpServers":{"x":{"url":"https://"}}}`,
		"padded url":        `{"mcpServers":{"x":{"url":" https://x.test"}}}`,
		"url not a string":  `{"mcpServers":{"x":{"url":1}}}`,
		"stdio type":        `{"mcpServers":{"x":{"type":"stdio","url":"https://x.test"}}}`,
		"header injection":  `{"mcpServers":{"x":{"url":"https://x.test","headers":{"A":"b\r\nX: y"}}}}`,
		"bad header name":   `{"mcpServers":{"x":{"url":"https://x.test","headers":{"A B":"c"}}}}`,
		"header not string": `{"mcpServers":{"x":{"url":"https://x.test","headers":{"A":1}}}}`,
		"disabled string":   `{"mcpServers":{"x":{"url":"https://x.test","disabled":"true"}}}`,
		"server not object": `{"mcpServers":{"x":"https://x.test"}}`,
		"servers array":     `{"mcpServers":[1]}`,
		"other top key":     `{"mcpServers":{},"inputs":[]}`,
		"empty name":        `{"mcpServers":{"":{"url":"https://x.test"}}}`,
		"padded name":       `{"mcpServers":{" x":{"url":"https://x.test"}}}`,
		"long name":         `{"mcpServers":{"` + strings.Repeat("n", MaxMCPServerName+1) + `":{"url":"https://x.test"}}}`,
		"not an object":     `[1]`,
		"not json":          `{"mcpServers":`,
		"too large":         `{"mcpServers":{"x":{"url":"https://x.test/` + strings.Repeat("a", MaxScopeMCPConfigBytes) + `"}}}`,
	}
	for name, raw := range refused {
		_, err := NormalizeRemoteMCPConfig(json.RawMessage(raw))
		var invalid *InvalidMCPConfigError
		if !errors.Is(err, ErrInvalidInput) || !errors.As(err, &invalid) || invalid.Reason == "" {
			t.Errorf("%s: err=%v", name, err)
		}
	}
}

// The enabled switch of a prompt component round-trips through the store and
// counts as a change of the component.
func TestPromptComponentEnabledStore(t *testing.T) {
	f := openStoreTx(t)
	tenantMigrated(t, f)
	ctx := context.Background()
	actor := f.bindIdentity(t, "org-home", "")
	write := PromptComponentsWrite{WorkspaceID: f.workspaceID, AgentID: f.agentID, ScopeType: ScopeScene, OrgID: "org-home", ScopeKey: "cidEnabled",
		Components: []PromptComponentInput{{Name: "on", Text: "applies"}, {Name: "off", Order: 1, Text: "stored only", Disabled: true}}}
	stored, err := ReplacePromptComponents(ctx, f.tx, write)
	if err != nil || len(stored) != 2 || stored[0].Name != "on" || stored[0].Disabled || stored[1].Name != "off" || !stored[1].Disabled || stored[1].UpdatedBy != "" {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	// Switching "off" on is a change (new stamp); "on" stays untouched.
	write.Components = []PromptComponentInput{{Name: "on", Text: "applies"}, {Name: "off", Order: 1, Text: "stored only"}}
	write.ActorID = actor
	again, err := ReplacePromptComponents(ctx, f.tx, write)
	if err != nil || len(again) != 2 || again[0].UpdatedBy != "" || again[1].Disabled || again[1].UpdatedBy != actor || again[1].ID != stored[1].ID {
		t.Fatalf("again=%+v err=%v", again, err)
	}
	// LoadLayers reads disabled components too; the merge leaves them out.
	write.Components = []PromptComponentInput{{Name: "on", Text: "applies", Disabled: true}}
	if _, err := ReplacePromptComponents(ctx, f.tx, write); err != nil {
		t.Fatal(err)
	}
	layers, err := LoadLayers(ctx, f.tx, f.workspaceID, f.agentID, LayerSelection{OrgID: "org-home", SceneKey: "cidEnabled"})
	if err != nil || len(layers) != 1 || len(layers[0].Prompts) != 1 || !layers[0].Prompts[0].Disabled {
		t.Fatalf("layers=%+v err=%v", layers, err)
	}
	if merged := MergeContext(layers...); len(merged.Prompts) != 0 || merged.PromptBlock() != "" {
		t.Fatalf("merged=%+v", merged)
	}

	// A client older than the switch omits it (KeepSwitch): an existing
	// component keeps its stored switch, a new one starts enabled.
	write.Components = []PromptComponentInput{
		{Name: "on", Text: "edited by an old client", KeepSwitch: true},
		{Name: "fresh", Order: 1, Text: "new", Disabled: true, KeepSwitch: true},
	}
	kept, err := ReplacePromptComponents(ctx, f.tx, write)
	if err != nil || len(kept) != 2 || kept[0].Name != "on" || !kept[0].Disabled || kept[0].Text != "edited by an old client" ||
		kept[1].Name != "fresh" || kept[1].Disabled {
		t.Fatalf("kept=%+v err=%v", kept, err)
	}
}
