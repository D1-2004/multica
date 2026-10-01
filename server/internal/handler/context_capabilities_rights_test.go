package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/contextcap"
)

// The one edit-rights policy: managers edit org and group scopes, only the
// person edits a person scope, link holders and everyone else only view.
func TestContextCapScopeRightsTable(t *testing.T) {
	none, all, sceneAll := contextCapRights{}, contextCapAllRights, contextCapSceneRights
	for _, tc := range []struct {
		scopeType     string
		manages, self bool
		want          contextCapRights
	}{
		{contextcap.ScopeOrg, true, false, all},
		{contextcap.ScopeOrg, true, true, all},
		{contextcap.ScopeOrg, false, false, none},
		{contextcap.ScopeOrg, false, true, none},
		{contextcap.ScopeScene, true, false, sceneAll},
		{contextcap.ScopeScene, true, true, sceneAll},
		{contextcap.ScopeScene, false, false, none},
		{contextcap.ScopeScene, false, true, none},
		{contextcap.ScopePerson, true, false, none},
		{contextcap.ScopePerson, false, true, all},
		{contextcap.ScopePerson, true, true, all},
		{contextcap.ScopePerson, false, false, none},
		{contextcap.ScopeOffer, true, true, none},
		{"", true, true, none},
	} {
		if got := contextCapScopeRights(tc.scopeType, tc.manages, tc.self); got != tc.want {
			t.Errorf("rights(%q, manages=%v, self=%v) = %+v, want %+v", tc.scopeType, tc.manages, tc.self, got, tc.want)
		}
	}

	// contextCapScopeAllows refuses a missing right with the scope's code.
	scope := func(scopeType string, rights contextCapRights) contextCapScope {
		return contextCapScope{Grant: contextcap.Grant{ScopeType: scopeType}, Rights: rights}
	}
	for _, tc := range []struct {
		name   string
		scope  contextCapScope
		need   contextCapNeed
		status int
		code   string
	}{
		{"read without rights", scope(contextcap.ScopeScene, none), contextCapNeedRead, http.StatusOK, ""},
		{"group toggle", scope(contextcap.ScopeScene, none), contextCapNeedToggle, http.StatusForbidden, contextCapErrManagerOnly},
		{"group connect", scope(contextcap.ScopeScene, none), contextCapNeedCredential, http.StatusForbidden, contextCapErrManagerOnly},
		{"org prompts", scope(contextcap.ScopeOrg, none), contextCapNeedPrompts, http.StatusForbidden, contextCapErrManagerOnly},
		{"person MCP", scope(contextcap.ScopePerson, none), contextCapNeedMCP, http.StatusForbidden, contextCapErrPersonOnly},
		{"person with rights", scope(contextcap.ScopePerson, all), contextCapNeedMCP, http.StatusOK, ""},
		{"only prompts", scope(contextcap.ScopeScene, contextCapRights{EditPrompts: true}), contextCapNeedToggle, http.StatusForbidden, contextCapErrManagerOnly},
		{"revoke needs no right", scope(contextcap.ScopePerson, none), contextCapNeedRevoke, http.StatusOK, ""},
	} {
		w := httptest.NewRecorder()
		allowed := contextCapScopeAllows(w, tc.scope, tc.need)
		if allowed != (tc.status == http.StatusOK) || (!allowed && (w.Code != tc.status || catalogErrorCode(t, w) != tc.code)) {
			t.Errorf("%s: allowed=%v status=%d body=%s", tc.name, allowed, w.Code, w.Body.String())
		}
	}
}

// ctxcapConfigDetail is the configure page's agent detail as far as the
// rights, prompt components and custom MCP servers go.
type ctxcapConfigDetail struct {
	Person *struct {
		ScopeKey          string                `json:"scope_key"`
		Rights            contextCapRights      `json:"rights"`
		Prompts           []contextCapPromptDTO `json:"prompts"`
		MCPConfig         json.RawMessage       `json:"mcp_config"`
		MCPConfigRedacted bool                  `json:"mcp_config_redacted"`
	} `json:"person"`
	Org *contextCapOrgLayerDTO `json:"org"`
}

// The configure page under the strict scheme: a group's link holder only
// views the group; a manager edits the group and the enterprise level; the
// person edits their own level, which a manager only views (also through the
// person's 1:1 chat). Every write path answers 403 with the scope's code.
func TestContextCapStrictRightsOnTheConfigurePage(t *testing.T) {
	f := newCtxcapFixture(t)
	f.cleanupScenes(t)
	f.cleanupGrantsAndLinks(t)
	router := ctxcapRouter(f.h)
	agentID := uuidToString(f.agent)
	base := "/api/context-capabilities/agents/" + agentID
	manager, holder, alice := testUserID, uuid.NewString(), uuid.NewString()
	f.grant(t, holder, contextcap.ScopeScene, ctxcapScene, "Ctxcap group")
	f.grant(t, alice, contextcap.ScopePerson, ctxcapStaff, "Alice")
	servers := map[string]any{"mcpServers": map[string]any{"docs": map[string]any{"type": "http", "url": "https://docs.example.test/mcp"}}}
	call := func(user, method, path string, body any) *httptest.ResponseRecorder {
		return ctxcapMobile(t, router, method, base+path, user, body)
	}
	prompts := func(scopeType, key string, list ...map[string]any) map[string]any {
		return map[string]any{"scope_type": scopeType, "scope_key": key, "prompts": list}
	}
	mcp := func(scopeType, key string) map[string]any {
		return map[string]any{"scope_type": scopeType, "scope_key": key, "mcp_config": servers}
	}
	sceneOf := func(user, key string) ctxcapSceneDetail {
		t.Helper()
		w := ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, key), user, nil)
		ctxcapExpectStatus(t, w, http.StatusOK, "scene "+key)
		var out ctxcapSceneDetail
		ctxcapDecode(t, w, &out)
		return out
	}
	detailOf := func(user string) ctxcapConfigDetail {
		t.Helper()
		w := ctxcapMobile(t, router, http.MethodGet, base, user, nil)
		ctxcapExpectStatus(t, w, http.StatusOK, "agent detail")
		var out ctxcapConfigDetail
		ctxcapDecode(t, w, &out)
		return out
	}
	expectRefused := func(code string, writes map[string]*httptest.ResponseRecorder) {
		t.Helper()
		for name, w := range writes {
			if w.Code != http.StatusForbidden || catalogErrorCode(t, w) != code {
				t.Fatalf("%s: %d %s, want 403 %s", name, w.Code, w.Body.String(), code)
			}
		}
	}

	// The group's link holder views the group and changes nothing in it.
	view := sceneOf(holder, ctxcapScene)
	if view.Rights != (contextCapRights{}) || view.CanConnect || view.Prompts == nil || len(view.Prompts) != 0 || !jsonNull(view.MCPConfig) {
		t.Fatalf("group view for its link holder = %+v", view)
	}
	expectRefused(contextCapErrManagerOnly, map[string]*httptest.ResponseRecorder{
		"holder binding": call(holder, http.MethodPut, "/bindings", map[string]any{"scope_type": "scene", "scope_key": ctxcapScene,
			"resource_type": "skill", "resource_id": f.skillScene, "enabled": false}),
		"holder credential": call(holder, http.MethodPut, "/credentials", map[string]any{"scope_type": "scene", "scope_key": ctxcapScene,
			"connector_id": f.global, "bearer": "holder-token-1234"}),
		"holder credential delete": call(holder, http.MethodDelete, "/credentials?scope_type=scene&scope_key="+url.QueryEscape(ctxcapScene)+"&connector_id="+f.scene, nil),
		"holder connect": call(holder, http.MethodPost, "/connections/start", map[string]any{"scope_type": "scene", "scope_key": ctxcapScene,
			"connector_id": f.scene}),
		"holder prompts":    call(holder, http.MethodPut, "/prompts", prompts("scene", ctxcapScene, map[string]any{"name": "tone", "text": "x"})),
		"holder mcp config": call(holder, http.MethodPut, "/mcp-config", mcp("scene", ctxcapScene)),
	})
	if stored, err := contextcap.ListScopeBindings(context.Background(), testPool, testWorkspaceID, agentID, contextcap.ScopeScene, ctxcapOrg, ctxcapScene); err != nil ||
		!ctxcapStoredBinding(stored, f.skillScene, true, "") {
		t.Fatalf("group bindings after the holder's refused write = %+v %v", stored, err)
	}

	// A manager edits the group: prompt components (one switched off) and
	// remote MCP servers; the link holder then reads them.
	if view = sceneOf(manager, ctxcapScene); view.Rights != contextCapSceneRights || !view.CanConnect {
		t.Fatalf("group view for a manager = %+v", view)
	}
	w := call(manager, http.MethodPut, "/prompts", prompts("scene", ctxcapScene,
		map[string]any{"name": "tone", "order": 1, "text": "group tone"},
		map[string]any{"name": "draft", "order": 2, "text": "not yet", "enabled": false}))
	ctxcapExpectStatus(t, w, http.StatusOK, "manager group prompts")
	var putPrompts struct {
		Prompts []agentContextPromptDTO `json:"prompts"`
	}
	ctxcapDecode(t, w, &putPrompts)
	if len(putPrompts.Prompts) != 2 || putPrompts.Prompts[0].Name != "tone" || !putPrompts.Prompts[0].Enabled || putPrompts.Prompts[0].ID == "" ||
		putPrompts.Prompts[1].Enabled || putPrompts.Prompts[1].UpdatedAt == "" {
		t.Fatalf("put prompts = %+v", putPrompts.Prompts)
	}
	w = call(manager, http.MethodPut, "/mcp-config", mcp("scene", ctxcapScene))
	ctxcapExpectStatus(t, w, http.StatusOK, "manager group MCP config")
	if !strings.Contains(w.Body.String(), "https://docs.example.test/mcp") {
		t.Fatalf("put MCP config = %s", w.Body.String())
	}
	// The holder reads the prompts; the MCP servers (whose headers and URLs
	// often carry tokens) are withheld from a caller who may not edit them.
	view = sceneOf(holder, ctxcapScene)
	if len(view.Prompts) != 2 || view.Prompts[0].Name != "tone" || !view.Prompts[0].Enabled || view.Prompts[1].Name != "draft" || view.Prompts[1].Enabled ||
		!jsonNull(view.MCPConfig) || !view.MCPConfigRedacted {
		t.Fatalf("group view after the manager's edits = %+v", view)
	}
	if view = sceneOf(manager, ctxcapScene); !strings.Contains(string(view.MCPConfig), "docs.example.test") || view.MCPConfigRedacted {
		t.Fatalf("group view for the manager after the edits = %+v", view)
	}

	// The person edits their own level; the detail carries it.
	ctxcapExpectStatus(t, call(alice, http.MethodPut, "/prompts", prompts("person", ctxcapStaff, map[string]any{"name": "me", "text": "person note"})),
		http.StatusOK, "person prompts")
	ctxcapExpectStatus(t, call(alice, http.MethodPut, "/mcp-config", mcp("person", ctxcapStaff)), http.StatusOK, "person MCP config")
	detail := detailOf(alice)
	if detail.Person == nil || detail.Person.Rights != contextCapAllRights || len(detail.Person.Prompts) != 1 || !detail.Person.Prompts[0].Enabled ||
		!strings.Contains(string(detail.Person.MCPConfig), "docs.example.test") || detail.Org != nil {
		t.Fatalf("person detail = %+v org=%+v", detail.Person, detail.Org)
	}

	// A 1:1 chat is a scene like a group (docs/agent-scene.md): a manager
	// edits it, the person views it and keeps their own person level, which
	// a manager still may not write.
	f.coordinatorDMJob(t, "cidCtxcapRightsDirect==", "Alice", ctxcapStaff, time.Minute)
	dm := f.sceneFor(t, ctxcapOrg, "dm", "cidCtxcapRightsDirect==", "", time.Minute)
	view = sceneOf(manager, dm)
	if view.Scope == nil || view.Scope.Type != contextcap.ScopeScene || view.Scope.Key != dm || view.Scene.Kind != "dm" ||
		view.Rights != contextCapSceneRights || !view.CanConnect {
		t.Fatalf("1:1 scene view for a manager = %+v", view)
	}
	ctxcapExpectStatus(t, call(manager, http.MethodPut, "/prompts", prompts("scene", dm, map[string]any{"name": "dm", "text": "1:1 tone"})),
		http.StatusOK, "manager 1:1 scene prompts")
	ctxcapExpectStatus(t, call(manager, http.MethodPut, "/prompts", prompts("person", ctxcapStaff, map[string]any{"name": "me", "text": "overwritten"})),
		http.StatusForbidden, "manager names the person scope")
	if stored, err := contextcap.ListPromptComponents(context.Background(), testPool, testWorkspaceID, agentID, contextcap.ScopePerson, ctxcapOrg, ctxcapStaff); err != nil ||
		len(stored) != 1 || stored[0].Text != "person note" {
		t.Fatalf("person prompts after the manager's refused write = %+v %v", stored, err)
	}
	// The person's link also granted the 1:1 scene: a view, no rights.
	f.grant(t, alice, contextcap.ScopeScene, dm, "Alice")
	if view = sceneOf(alice, dm); view.Rights != (contextCapRights{}) || len(view.Prompts) != 1 || view.Prompts[0].Text != "1:1 tone" {
		t.Fatalf("1:1 scene view for the person = %+v", view)
	}
	expectRefused(contextCapErrManagerOnly, map[string]*httptest.ResponseRecorder{
		"person 1:1 scene prompts": call(alice, http.MethodPut, "/prompts", prompts("scene", dm, map[string]any{"name": "dm", "text": "mine"})),
	})

	// The enterprise level: managers only, and nobody else sees it.
	ctxcapExpectStatus(t, call(manager, http.MethodPut, "/prompts", prompts("org", ctxcapOrg, map[string]any{"name": "rules", "text": "org rules"})),
		http.StatusOK, "manager org prompts")
	ctxcapExpectStatus(t, call(manager, http.MethodPut, "/mcp-config", mcp("org", ctxcapOrg)), http.StatusOK, "manager org MCP config")
	detail = detailOf(manager)
	if detail.Org == nil || detail.Org.Rights != contextCapAllRights || !detail.Org.CanEdit || len(detail.Org.Prompts) != 1 ||
		detail.Org.Prompts[0].Text != "org rules" || !strings.Contains(string(detail.Org.MCPConfig), "docs.example.test") {
		t.Fatalf("manager org layer = %+v", detail.Org)
	}
	for _, user := range []string{holder, alice} {
		expectRefused(contextCapErrManagerOnly, map[string]*httptest.ResponseRecorder{
			"org prompts":    call(user, http.MethodPut, "/prompts", prompts("org", ctxcapOrg, map[string]any{"name": "rules", "text": "x"})),
			"org mcp config": call(user, http.MethodPut, "/mcp-config", mcp("org", ctxcapOrg)),
			"org binding": call(user, http.MethodPut, "/bindings", map[string]any{"scope_type": "org", "scope_key": ctxcapOrg,
				"resource_type": "skill", "resource_id": f.skillScene, "enabled": true}),
		})
		if detail = detailOf(user); detail.Org != nil {
			t.Fatalf("org layer shown to a non-manager: %+v", detail.Org)
		}
	}

	// A workspace that always redacts secrets withholds custom MCP servers.
	ctx := context.Background()
	var settings []byte
	if err := testPool.QueryRow(ctx, `SELECT settings FROM workspace WHERE id = $1`, testWorkspaceID).Scan(&settings); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `UPDATE workspace SET settings = $2 WHERE id = $1`, testWorkspaceID, settings)
	})
	if _, err := testPool.Exec(ctx, `UPDATE workspace SET settings = COALESCE(settings, '{}'::jsonb) || '{"always_redact_env": true}'::jsonb WHERE id = $1`, testWorkspaceID); err != nil {
		t.Fatal(err)
	}
	if detail = detailOf(alice); detail.Person == nil || !jsonNull(detail.Person.MCPConfig) || !detail.Person.MCPConfigRedacted {
		t.Fatalf("redacted person detail = %+v", detail.Person)
	}
	if view = sceneOf(holder, ctxcapScene); !jsonNull(view.MCPConfig) || !view.MCPConfigRedacted || len(view.Prompts) != 2 {
		t.Fatalf("redacted group view = %+v", view)
	}
}

// The configure page's custom MCP servers are remote only, never a local
// command; prompts follow the admin rules, enabled defaulting to true.
func TestContextConfigRemoteMCPAndPromptValidation(t *testing.T) {
	f := newCtxcapFixture(t)
	f.cleanupGrantsAndLinks(t)
	router := ctxcapRouter(f.h)
	agentID := uuidToString(f.agent)
	alice := uuid.NewString()
	f.grant(t, alice, contextcap.ScopePerson, ctxcapStaff, "Alice")
	ctx := context.Background()
	put := func(what string, body any) *httptest.ResponseRecorder {
		return ctxcapMobile(t, router, http.MethodPut, "/api/context-capabilities/agents/"+agentID+"/"+what, alice, body)
	}
	withConfig := func(config any) map[string]any {
		return map[string]any{"scope_type": "person", "scope_key": ctxcapStaff, "mcp_config": config}
	}
	server := func(fields map[string]any) map[string]any {
		return map[string]any{"mcpServers": map[string]any{"x": fields}}
	}
	for name, config := range map[string]any{
		"command":            server(map[string]any{"command": "npx", "args": []string{"-y", "pkg"}}),
		"url and command":    server(map[string]any{"url": "https://x.example.test", "command": "sh"}),
		"args":               server(map[string]any{"url": "https://x.example.test", "args": []string{"a"}}),
		"env":                server(map[string]any{"url": "https://x.example.test", "env": map[string]string{"A": "b"}}),
		"cwd":                server(map[string]any{"url": "https://x.example.test", "cwd": "/tmp"}),
		"non-http url":       server(map[string]any{"url": "ftp://x.example.test"}),
		"stdio type":         server(map[string]any{"type": "stdio", "url": "https://x.example.test"}),
		"no url":             server(map[string]any{"type": "http"}),
		"header injection":   server(map[string]any{"url": "https://x.example.test", "headers": map[string]string{"A": "b\r\nX: y"}}),
		"reserved multica":   map[string]any{"mcpServers": map[string]any{"multica": map[string]any{"url": "https://x.example.test"}}},
		"reserved connector": map[string]any{"mcpServers": map[string]any{"c0123456789abcdef": map[string]any{"url": "https://x.example.test"}}},
		"array":              []any{1},
		"string":             "x",
		"servers array":      map[string]any{"mcpServers": []any{1}},
		"other top key":      map[string]any{"mcpServers": map[string]any{}, "inputs": []any{}},
	} {
		w := put("mcp-config", withConfig(config))
		if w.Code != http.StatusBadRequest || catalogErrorCode(t, w) != contextCapErrInvalidMCPConfig {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	for name, body := range map[string]any{
		"missing mcp_config": map[string]any{"scope_type": "person", "scope_key": ctxcapStaff},
		"unknown field":      map[string]any{"scope_type": "person", "scope_key": ctxcapStaff, "mcp_config": nil, "extra": 1},
		"not json":           `{"scope_type":`,
	} {
		ctxcapExpectStatus(t, put("mcp-config", body), http.StatusBadRequest, name)
	}
	if _, err := contextcap.GetScopeMCPConfig(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopePerson, ctxcapOrg, ctxcapStaff); !errors.Is(err, contextcap.ErrNotFound) {
		t.Fatalf("a refused config was stored: %v", err)
	}
	// A disabled remote server is stored with its switch; no server clears.
	w := put("mcp-config", withConfig(map[string]any{"mcpServers": map[string]any{"docs": map[string]any{
		"type": "http", "url": "https://docs.example.test/mcp", "headers": map[string]string{"Authorization": "Bearer t"}, "disabled": true,
	}}}))
	ctxcapExpectStatus(t, w, http.StatusOK, "disabled remote server")
	if stored, err := contextcap.GetScopeMCPConfig(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopePerson, ctxcapOrg, ctxcapStaff); err != nil ||
		!strings.Contains(string(stored.MCPConfig), `"disabled": true`) && !strings.Contains(string(stored.MCPConfig), `"disabled":true`) {
		t.Fatalf("stored config = %s %v", stored.MCPConfig, err)
	}
	w = put("mcp-config", withConfig(map[string]any{"mcpServers": map[string]any{}}))
	ctxcapExpectStatus(t, w, http.StatusOK, "clear")
	if strings.TrimSpace(w.Body.String()) != `{"mcp_config":null}` {
		t.Fatalf("clear response = %s", w.Body.String())
	}
	if _, err := contextcap.GetScopeMCPConfig(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopePerson, ctxcapOrg, ctxcapStaff); !errors.Is(err, contextcap.ErrNotFound) {
		t.Fatalf("cleared config still stored: %v", err)
	}

	withPrompts := func(list any) map[string]any {
		return map[string]any{"scope_type": "person", "scope_key": ctxcapStaff, "prompts": list}
	}
	many := make([]map[string]any, contextcap.MaxPromptComponents+1)
	for i := range many {
		many[i] = map[string]any{"name": strings.Repeat("n", i+1), "text": "t"}
	}
	for _, tc := range []struct {
		name string
		body any
		code string
	}{
		{"missing prompts", map[string]any{"scope_type": "person", "scope_key": ctxcapStaff}, ""},
		{"duplicate names", withPrompts([]map[string]any{{"name": "a", "text": "x"}, {"name": " a", "text": "y"}}), agentContextErrDuplicate},
		{"empty text", withPrompts([]map[string]any{{"name": "a", "text": " "}}), agentContextErrPrompts},
		{"too many", withPrompts(many), agentContextErrPrompts},
		{"enabled not a boolean", withPrompts([]map[string]any{{"name": "a", "text": "x", "enabled": "no"}}), ""},
		{"unknown prompt field", withPrompts([]map[string]any{{"name": "a", "text": "x", "id": "1"}}), ""},
		{"prompts not a list", withPrompts("a"), ""},
	} {
		w := put("prompts", tc.body)
		if w.Code != http.StatusBadRequest || (tc.code != "" && catalogErrorCode(t, w) != tc.code) {
			t.Fatalf("%s: %d %s", tc.name, w.Code, w.Body.String())
		}
	}
	w = put("prompts", withPrompts([]map[string]any{{"name": "a", "text": "x"}}))
	ctxcapExpectStatus(t, w, http.StatusOK, "prompt without enabled")
	var got struct {
		Prompts []agentContextPromptDTO `json:"prompts"`
	}
	ctxcapDecode(t, w, &got)
	if len(got.Prompts) != 1 || !got.Prompts[0].Enabled {
		t.Fatalf("prompt without enabled = %+v", got.Prompts)
	}
}

// The admin Context Builder follows the same rights (a manager views a
// person level, edits org and group levels), and a switched-off prompt
// component or custom MCP server takes no part in the 生效预览 nor in the
// claim: it neither applies nor overrides the outer one.
func TestContextBuilderDisabledComponentsAndNodeRights(t *testing.T) {
	b := newCtxBuilder(t)
	b.cleanupScenes(t)
	b.configureIdentityTenant(t)
	personPath := ctxNodePath(b.agentID, ctxcapOrg, contextcap.ScopePerson, ctxcapStaff)

	for _, tc := range []struct {
		scopeType, key string
		want           contextCapRights
	}{
		{contextcap.ScopeOrg, ctxcapOrg, contextCapAllRights},
		{contextcap.ScopeScene, ctxcapScene, contextCapSceneRights},
		{contextcap.ScopePerson, ctxcapStaff, contextCapRights{}},
	} {
		node := ctxNode(t, b.router, "", b.agentID, tc.scopeType, tc.key)
		if node.Rights != tc.want || node.CanEdit != tc.want.Toggle || node.CanConnect != tc.want.Connect {
			t.Fatalf("%s node rights = %+v can_edit=%v can_connect=%v", tc.scopeType, node.Rights, node.CanEdit, node.CanConnect)
		}
	}
	for name, w := range map[string]*httptest.ResponseRecorder{
		"prompts":    scenesAs(t, b.router, "", http.MethodPut, personPath+"/prompts", ctxBuilderPrompts([3]any{"me", 3, "manager note"})),
		"mcp config": scenesAs(t, b.router, "", http.MethodPut, personPath+"/mcp-config", ctxBuilderServers(map[string]string{"notes": "https://manager.example.test"})),
		"binding": scenesAs(t, b.router, "", http.MethodPut, personPath+"/bindings",
			map[string]any{"resource_type": "connector", "resource_id": b.person, "enabled": false}),
		"credential":        scenesAs(t, b.router, "", http.MethodPut, personPath+"/credentials", map[string]string{"connector_id": b.scene, "bearer": "manager-token-1"}),
		"credential delete": scenesAs(t, b.router, "", http.MethodDelete, personPath+"/credentials?connector_id="+b.scene, nil),
		"connect":           scenesAs(t, b.router, "", http.MethodPost, personPath+"/connections/start", map[string]string{"connector_id": b.scene}),
	} {
		if w.Code != http.StatusForbidden || catalogErrorCode(t, w) != contextCapErrPersonOnly {
			t.Fatalf("manager %s on a person node: %d %s", name, w.Code, w.Body.String())
		}
	}

	// Switch the group's tone prompt and crm server off.
	b.put(t, ctxcapOrg, contextcap.ScopeScene, ctxcapScene, "prompts", map[string]any{"prompts": []map[string]any{
		{"name": "tone", "order": 1, "text": "group tone", "enabled": false},
	}})
	b.put(t, ctxcapOrg, contextcap.ScopeScene, ctxcapScene, "mcp-config", map[string]any{"mcp_config": map[string]any{"mcpServers": map[string]any{
		"crm": map[string]any{"type": "http", "url": "https://group-crm.example.test", "disabled": true},
	}}})
	node := ctxNode(t, b.router, "", b.agentID, contextcap.ScopeScene, ctxcapScene)
	if len(node.Prompts) != 1 || node.Prompts[0].Enabled || !strings.Contains(string(node.MCPConfig), "disabled") {
		t.Fatalf("group node own components = %+v %s", node.Prompts, node.MCPConfig)
	}
	for _, p := range node.Effective.Prompts {
		if p.Layer == contextcap.LayerScene || p.OverriddenBy != "" {
			t.Fatalf("preview prompts with the group's tone off = %+v", node.Effective.Prompts)
		}
	}
	for _, s := range node.Effective.MCPServers {
		if s.Layer == contextcap.LayerScene || (s.Name == "crm" && s.OverriddenBy != "") {
			t.Fatalf("preview servers with the group's crm off = %+v", node.Effective.MCPServers)
		}
	}
	agent := b.claim(t, b.task(t, ctxBuilderGroupTask(ctxcapStaff, ctxcapStaff)))
	if strings.Contains(agent.Instructions, "group tone") || !strings.Contains(agent.Instructions, "### tone\n\norg tone") {
		t.Fatalf("claim instructions with the group's tone off = %q", agent.Instructions)
	}
	if servers := ctxBuilderMCPServers(t, agent.McpConfig); servers["crm"] != "https://org-crm.example.test" || strings.Contains(string(agent.McpConfig), "disabled") {
		t.Fatalf("claim MCP config with the group's crm off = %s", agent.McpConfig)
	}

	// A client that omits the switch keeps the stored one (off) ...
	b.put(t, ctxcapOrg, contextcap.ScopeScene, ctxcapScene, "prompts", ctxBuilderPrompts([3]any{"tone", 1, "group tone"}))
	if node = ctxNode(t, b.router, "", b.agentID, contextcap.ScopeScene, ctxcapScene); len(node.Prompts) != 1 || node.Prompts[0].Enabled {
		t.Fatalf("group prompts after a save without the switch = %+v", node.Prompts)
	}
	// ... switched back on, both override again.
	b.put(t, ctxcapOrg, contextcap.ScopeScene, ctxcapScene, "prompts", map[string]any{"prompts": []map[string]any{
		{"name": "tone", "order": 1, "text": "group tone", "enabled": true},
	}})
	b.put(t, ctxcapOrg, contextcap.ScopeScene, ctxcapScene, "mcp-config", ctxBuilderServers(map[string]string{"crm": "https://group-crm.example.test"}))
	agent = b.claim(t, b.task(t, ctxBuilderGroupTask(ctxcapStaff, ctxcapStaff)))
	if !strings.Contains(agent.Instructions, "### tone\n\ngroup tone") || ctxBuilderMCPServers(t, agent.McpConfig)["crm"] != "https://group-crm.example.test" {
		t.Fatalf("claim with the group's components on = %q %s", agent.Instructions, agent.McpConfig)
	}

	// Managers still revoke a person's configure-page access, which they may
	// not edit otherwise.
	w := scenesAs(t, b.router, "", http.MethodDelete, personPath+"/grants", nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "revoke the person's grants")
	if !strings.Contains(w.Body.String(), `"revoked":1`) {
		t.Fatalf("revoke = %s", w.Body.String())
	}
}
