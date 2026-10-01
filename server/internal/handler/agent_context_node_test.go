package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/contextcap"
)

const (
	nodeDirect      = "cidContextNodeDirect=="
	nodeDirectStaff = "staff-context-node-dora"
)

// registerGroupScene records a group scene the agent has seen (an
// agent_scene_config row) under orgID.
func registerGroupScene(t *testing.T, agentID, orgID, sceneKey, title string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `INSERT INTO agent_scene_config (workspace_id, agent_id, platform, org_id, scene_key, scene_kind, scene_title)
		VALUES ($1, $2, 'dingtalk', $3, $4, 'group', $5)`, testWorkspaceID, agentID, orgID, sceneKey, title); err != nil {
		t.Fatal(err)
	}
}

// jsonNull reports whether a decoded JSON value is null or absent.
func jsonNull(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed == "" || trimmed == "null"
}

// A node's configuration lives where the runtime reads it: a group's own
// scope, or a 1:1 chat's person. The node shows that scope, its custom MCP
// servers and the credential of each offered connector, and says whether
// the caller may connect accounts there (managers for groups; only the
// person for a person).
func TestAgentContextNodeLivesInTheScopeOfTheScene(t *testing.T) {
	f := newCtxcapFixture(t)
	f.cleanupScenes(t)
	f.cleanupGrantsAndLinks(t)
	router := scenesRouter(f.h)
	agentID := uuidToString(f.agent)
	ctx := context.Background()
	scenePath := func(key string) string { return ctxNodePath(agentID, ctxcapOrg, contextcap.ScopeScene, key) }
	servers := map[string]any{"mcpServers": map[string]any{"docs": map[string]any{"type": "http", "url": "https://docs.example.test/mcp"}}}

	// A group scene: its own scope; a manager connects its accounts.
	node := ctxNode(t, router, "", agentID, contextcap.ScopeScene, ctxcapScene)
	if node.Scope == nil || node.Scope.Type != contextcap.ScopeScene || node.Scope.Key != ctxcapScene || node.Scope.OrgID != ctxcapOrg ||
		!node.CanConnect || !jsonNull(node.MCPConfig) || node.MCPConfigRedacted {
		t.Fatalf("group node = %+v", node)
	}
	bearer := ctxNodeConnector(t, node, f.scene)
	if !bearer.AcceptsCredential || bearer.AcceptsPAT || bearer.OAuthAvailable || bearer.InstallURL != "" || bearer.Credential.Connected || !bearer.Enabled {
		t.Fatalf("offered bearer connector = %+v", bearer)
	}
	if none := ctxNodeConnector(t, node, f.person); none.AcceptsCredential || none.Credential.Connected || none.Enabled {
		t.Fatalf("offered no-auth connector = %+v", none)
	}
	credentialsPath := scenePath(ctxcapScene) + "/credentials"
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, credentialsPath, map[string]string{"connector_id": f.scene, "bearer": "scene-config-token"}),
		http.StatusOK, "manager stores a group credential")
	node = ctxNode(t, router, "", agentID, contextcap.ScopeScene, ctxcapScene)
	if got := ctxNodeConnector(t, node, f.scene).Credential; got != (agentSceneCredentialDTO{Connected: true, Account: "••••oken"}) {
		t.Fatalf("group credential = %+v", got)
	}
	if strings.Contains(scenesAs(t, router, "", http.MethodGet, scenePath(ctxcapScene), nil).Body.String(), "scene-config-token") {
		t.Fatal("context node leaks the credential")
	}
	for name, body := range map[string]any{
		"not offered": map[string]string{"connector_id": f.notOffered, "bearer": "x-token-1"},
		"no-auth":     map[string]string{"connector_id": f.person, "bearer": "x-token-1"},
		"bad bearer":  map[string]string{"connector_id": f.scene, "bearer": " x"},
	} {
		if w := scenesAs(t, router, "", http.MethodPut, credentialsPath, body); w.Code != http.StatusForbidden && w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodDelete, credentialsPath+"?connector_id="+f.scene, nil), http.StatusNoContent, "manager removes a group credential")
	if got := ctxNodeConnector(t, ctxNode(t, router, "", agentID, contextcap.ScopeScene, ctxcapScene), f.scene).Credential; got.Connected {
		t.Fatalf("credential after delete = %+v", got)
	}

	// Custom MCP servers of the group: stored in the scene scope.
	w := scenesAs(t, router, "", http.MethodPut, scenePath(ctxcapScene)+"/mcp-config", map[string]any{"mcp_config": servers})
	ctxcapExpectStatus(t, w, http.StatusOK, "put group MCP config")
	if !strings.Contains(w.Body.String(), "https://docs.example.test/mcp") {
		t.Fatalf("put response = %s", w.Body.String())
	}
	if stored, err := contextcap.GetScopeMCPConfig(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopeScene, ctxcapOrg, ctxcapScene); err != nil ||
		!strings.Contains(string(stored.MCPConfig), "docs.example.test") || stored.UpdatedBy != testUserID {
		t.Fatalf("stored group MCP config = %+v %v", stored, err)
	}
	if node = ctxNode(t, router, "", agentID, contextcap.ScopeScene, ctxcapScene); !strings.Contains(string(node.MCPConfig), "docs.example.test") {
		t.Fatalf("group node MCP config = %s", node.MCPConfig)
	}
	for name, body := range map[string]any{
		"missing field": map[string]any{},
		"array":         map[string]any{"mcp_config": []any{1}},
		"string":        map[string]any{"mcp_config": "x"},
		"unknown field": map[string]any{"mcp_config": servers, "scope": "person"},
		"too large":     map[string]any{"mcp_config": map[string]any{"x": strings.Repeat("a", contextcap.MaxScopeMCPConfigBytes)}},
	} {
		ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, scenePath(ctxcapScene)+"/mcp-config", body), http.StatusBadRequest, name)
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, scenePath("cidContextNodeNeverSeen==")+"/mcp-config", map[string]any{"mcp_config": servers}), http.StatusNotFound, "unknown scene")
	member := createPermissionTestMember(t, "context-node-member-"+uuid.NewString()[:8]+"@example.test")
	owner := createPermissionTestMember(t, "context-node-owner-"+uuid.NewString()[:8]+"@example.test")
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE user_id = ANY($1::uuid[])`, []string{member, owner})
	})
	ctxcapExpectStatus(t, scenesAs(t, router, member, http.MethodPut, scenePath(ctxcapScene)+"/mcp-config", map[string]any{"mcp_config": servers}), http.StatusForbidden, "plain member")
	ctxcapExpectStatus(t, scenesAs(t, router, member, http.MethodGet, scenePath(ctxcapScene), nil), http.StatusForbidden, "plain member read")
	w = scenesAs(t, router, "", http.MethodPut, scenePath(ctxcapScene)+"/mcp-config", map[string]any{"mcp_config": nil})
	ctxcapExpectStatus(t, w, http.StatusOK, "clear group MCP config")
	if strings.TrimSpace(w.Body.String()) != `{"mcp_config":null}` {
		t.Fatalf("clear response = %s", w.Body.String())
	}

	// A 1:1 chat whose person is unknown: no configuration, writes refused.
	if err := contextcap.RegisterDirectScene(ctx, testPool, testWorkspaceID, agentID, ctxcapOrg, nodeDirect, "Dora"); err != nil {
		t.Fatal(err)
	}
	node = ctxNode(t, router, "", agentID, contextcap.ScopeScene, nodeDirect)
	if node.Scope != nil || node.CanConnect || len(ctxNodeEnabled(node)) != 0 || !jsonNull(node.MCPConfig) || node.Scene == nil || node.Scene.Kind != "dm" {
		t.Fatalf("DM of an unknown person = %+v", node)
	}
	for name, w := range map[string]*httptest.ResponseRecorder{
		"mcp config": scenesAs(t, router, "", http.MethodPut, scenePath(nodeDirect)+"/mcp-config", map[string]any{"mcp_config": servers}),
		"binding": scenesAs(t, router, "", http.MethodPut, scenePath(nodeDirect)+"/bindings",
			map[string]any{"resource_type": "connector", "resource_id": f.person, "enabled": true}),
		"prompts": scenesAs(t, router, "", http.MethodPut, scenePath(nodeDirect)+"/prompts",
			map[string]any{"prompts": []map[string]any{{"name": "a", "text": "b"}}}),
	} {
		if w.Code != http.StatusConflict || catalogErrorCode(t, w) != contextCapErrDMPersonUnknown {
			t.Fatalf("%s write to a DM of an unknown person: %d %s", name, w.Code, w.Body.String())
		}
	}

	// Dora writes in the chat: her personal scope is the DM's configuration.
	f.coordinatorDMJob(t, nodeDirect, "Dora", nodeDirectStaff, time.Minute)
	node = ctxNode(t, router, "", agentID, contextcap.ScopeScene, nodeDirect)
	if node.Scope == nil || *node.Scope != (agentContextScopeDTO{Type: contextcap.ScopePerson, OrgID: ctxcapOrg, Key: nodeDirectStaff, Title: "Dora"}) || node.CanConnect {
		t.Fatalf("DM of Dora for a manager = %+v", node)
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, scenePath(nodeDirect)+"/mcp-config", map[string]any{"mcp_config": servers}), http.StatusOK, "DM MCP config")
	if _, err := contextcap.GetScopeMCPConfig(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopePerson, ctxcapOrg, nodeDirectStaff); err != nil {
		t.Fatalf("DM MCP config not in the person scope: %v", err)
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, scenePath(nodeDirect)+"/prompts",
		map[string]any{"prompts": []map[string]any{{"name": "语气", "text": "Be brief."}}}), http.StatusOK, "DM prompts")
	if prompts, err := contextcap.ListPromptComponents(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopePerson, ctxcapOrg, nodeDirectStaff); err != nil ||
		len(prompts) != 1 || prompts[0].Text != "Be brief." {
		t.Fatalf("DM prompts not in the person scope: %+v %v", prompts, err)
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, scenePath(nodeDirect)+"/bindings",
		map[string]any{"resource_type": "connector", "resource_id": f.person, "enabled": true}), http.StatusOK, "DM binding")
	if stored, err := contextcap.ListScopeBindings(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopePerson, ctxcapOrg, nodeDirectStaff); err != nil ||
		!ctxcapStoredBinding(stored, f.person, true, testUserID) {
		t.Fatalf("DM binding not in the person scope: %+v %v", stored, err)
	}
	// The person node is the same scope, with Dora's 1:1 chat as its scene.
	person := ctxNode(t, router, "", agentID, contextcap.ScopePerson, nodeDirectStaff)
	if person.Scope == nil || person.Scope.Key != nodeDirectStaff || person.Scene == nil || person.Scene.SceneKey != nodeDirect ||
		len(person.Prompts) != 1 || !ctxNodeEnabled(person)[f.person] {
		t.Fatalf("person node = %+v", person)
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodGet, ctxNodePath(agentID, ctxcapOrg, contextcap.ScopePerson, "staff-nobody"), nil), http.StatusNotFound, "unknown person")
	// A manager never stores or connects Dora's own account.
	w = scenesAs(t, router, "", http.MethodPut, scenePath(nodeDirect)+"/credentials", map[string]string{"connector_id": f.scene, "bearer": "manager-token"})
	if w.Code != http.StatusForbidden || catalogErrorCode(t, w) != contextCapErrPersonOnly {
		t.Fatalf("manager credential on a person: %d %s", w.Code, w.Body.String())
	}

	// Dora's own account: its hint reaches workspace admins and Dora, not
	// an agent owner who is only a member.
	key := contextcap.CredentialBinding{WorkspaceID: testWorkspaceID, AgentID: agentID, ConnectorID: f.scene,
		ScopeType: contextcap.ScopePerson, OrgID: ctxcapOrg, ScopeKey: nodeDirectStaff}
	sealed, err := contextcap.SealCredential(f.box, key, "dora-personal-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contextcap.UpsertCredential(ctx, testPool, key, sealed, contextcap.Hint("dora-personal-token"), ""); err != nil {
		t.Fatal(err)
	}
	node = ctxNode(t, router, "", agentID, contextcap.ScopeScene, nodeDirect)
	if got := ctxNodeConnector(t, node, f.scene).Credential; got != (agentSceneCredentialDTO{Connected: true, Account: "••••oken"}) || !strings.Contains(string(node.MCPConfig), "docs") {
		t.Fatalf("DM node for a workspace admin: credential=%+v mcp=%s", got, node.MCPConfig)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent SET owner_id = $1 WHERE id = $2`, owner, agentID); err != nil {
		t.Fatal(err)
	}
	node = ctxNode(t, router, owner, agentID, contextcap.ScopeScene, nodeDirect)
	if got := ctxNodeConnector(t, node, f.scene).Credential; got != (agentSceneCredentialDTO{Connected: true}) || node.CanConnect {
		t.Fatalf("DM node for the agent owner: credential=%+v can_connect=%v", got, node.CanConnect)
	}
	// Dora is the agent owner: her own account and connect.
	f.grant(t, owner, contextcap.ScopePerson, nodeDirectStaff, "Dora")
	node = ctxNode(t, router, owner, agentID, contextcap.ScopeScene, nodeDirect)
	if got := ctxNodeConnector(t, node, f.scene).Credential; got.Account != "••••oken" || !node.CanConnect {
		t.Fatalf("DM node for Dora: credential=%+v can_connect=%v", got, node.CanConnect)
	}
	ctxcapExpectStatus(t, scenesAs(t, router, owner, http.MethodPut, ctxNodePath(agentID, ctxcapOrg, contextcap.ScopePerson, nodeDirectStaff)+"/credentials",
		map[string]string{"connector_id": f.scene, "bearer": "dora-new-token"}), http.StatusOK, "Dora replaces her token")

	// A workspace that always redacts secrets withholds custom MCP servers.
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
	node = ctxNode(t, router, "", agentID, contextcap.ScopeScene, nodeDirect)
	if !jsonNull(node.MCPConfig) || !node.MCPConfigRedacted {
		t.Fatalf("redacted DM node: mcp=%s redacted=%v", node.MCPConfig, node.MCPConfigRedacted)
	}
}

// The org node and the effective preview: global -> org -> scene/person,
// prompt components and custom MCP servers nearest layer wins by name,
// connectors and skills a union.
func TestAgentContextNodeOrgAndEffective(t *testing.T) {
	f := newCtxcapFixture(t)
	f.cleanupScenes(t)
	f.cleanupGrantsAndLinks(t)
	router := scenesRouter(f.h)
	agentID := uuidToString(f.agent)
	orgPath := ctxNodePath(agentID, ctxcapOrg, contextcap.ScopeOrg, ctxcapOrg)
	scenePath := ctxNodePath(agentID, ctxcapOrg, contextcap.ScopeScene, ctxcapScene)
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET mcp_config = $2 WHERE id = $1`, agentID,
		`{"mcpServers":{"docs":{"url":"https://global.example.test"},"base":{"command":"x"}}}`); err != nil {
		t.Fatal(err)
	}

	node := ctxNode(t, router, "", agentID, contextcap.ScopeOrg, ctxcapOrg)
	if node.Scope == nil || node.Scope.Type != contextcap.ScopeOrg || node.Scope.Key != ctxcapOrg || node.Scene != nil || !node.CanConnect ||
		len(ctxNodeEnabled(node)) != 0 {
		t.Fatalf("org node = %+v", node)
	}
	// Global only: the granted connector, the agent skill, the agent's MCP servers.
	if len(node.Effective.Connectors) != 1 || node.Effective.Connectors[0].ID != f.global || node.Effective.Connectors[0].Layer != contextcap.LayerGlobal ||
		len(node.Effective.Skills) != 1 || node.Effective.Skills[0].ID != f.skillAgent || len(node.Effective.MCPServers) != 2 || len(node.Effective.Prompts) != 0 {
		t.Fatalf("org node effective = %+v", node.Effective)
	}
	// The granted Bearer connector is a 通用能力: listed at every level as
	// global, never switched there, and the level may give it its own token.
	if granted := ctxNodeConnector(t, node, f.global); !granted.Global || granted.Enabled || !granted.AcceptsCredential {
		t.Fatalf("granted connector on the org node = %+v", granted)
	}
	if offered := ctxNodeConnector(t, node, f.scene); offered.Global {
		t.Fatalf("offered connector marked global: %+v", offered)
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, orgPath+"/credentials", map[string]string{"connector_id": f.global, "bearer": "org-global-token"}),
		http.StatusOK, "org credential for a granted connector")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, orgPath+"/bindings", map[string]any{"resource_type": "connector", "resource_id": f.global, "enabled": true}),
		http.StatusForbidden, "binding a granted, unoffered connector")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodGet, ctxNodePath(agentID, ctxcapOrg, contextcap.ScopeOrg, "org-else"), nil), http.StatusBadRequest, "org key mismatch")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodGet, ctxNodePath(agentID, ctxcapOrg, "channel", "x"), nil), http.StatusBadRequest, "unknown scope type")

	// Enterprise level: a prompt, a connector, custom MCP servers.
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, orgPath+"/prompts", map[string]any{"prompts": []map[string]any{
		{"name": "tone", "order": 0, "text": "org tone"}, {"name": "rules", "order": 1, "text": "org rules"},
	}}), http.StatusOK, "org prompts")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, orgPath+"/bindings", map[string]any{"resource_type": "connector", "resource_id": f.person, "enabled": true}),
		http.StatusOK, "org binding")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, orgPath+"/mcp-config", map[string]any{"mcp_config": map[string]any{"mcpServers": map[string]any{
		"docs": map[string]any{"url": "https://org.example.test"}, "crm": map[string]any{"url": "https://crm.example.test"},
	}}}), http.StatusOK, "org MCP config")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, orgPath+"/credentials", map[string]string{"connector_id": f.scene, "bearer": "org-level-token"}),
		http.StatusOK, "org credential")
	// Group level: overrides "tone" and "crm".
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, scenePath+"/prompts", map[string]any{"prompts": []map[string]any{
		{"name": "tone", "order": 0, "text": "group tone"},
	}}), http.StatusOK, "group prompts")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, scenePath+"/mcp-config", map[string]any{"mcp_config": map[string]any{"mcpServers": map[string]any{
		"crm": map[string]any{"url": "https://group-crm.example.test"},
	}}}), http.StatusOK, "group MCP config")

	node = ctxNode(t, router, "", agentID, contextcap.ScopeOrg, ctxcapOrg)
	if len(node.Prompts) != 2 || node.Prompts[0].Name != "tone" || !ctxNodeEnabled(node)[f.person] ||
		!ctxNodeConnector(t, node, f.scene).Credential.Connected || !strings.Contains(string(node.MCPConfig), "crm") {
		t.Fatalf("org node after writes = %+v", node)
	}

	scene := ctxNode(t, router, "", agentID, contextcap.ScopeScene, ctxcapScene)
	type promptView struct{ Name, Text, Layer, OverriddenBy string }
	prompts := []promptView{}
	for _, p := range scene.Effective.Prompts {
		prompts = append(prompts, promptView{p.Name, p.Text, p.Layer, p.OverriddenBy})
	}
	wantPrompts := []promptView{
		{"tone", "org tone", contextcap.LayerOrg, contextcap.LayerScene},
		{"tone", "group tone", contextcap.LayerScene, ""},
		{"rules", "org rules", contextcap.LayerOrg, ""},
	}
	if len(prompts) != len(wantPrompts) {
		t.Fatalf("scene effective prompts = %+v", prompts)
	}
	for i := range wantPrompts {
		if prompts[i] != wantPrompts[i] {
			t.Fatalf("scene effective prompts = %+v, want %+v", prompts, wantPrompts)
		}
	}
	servers := map[string]string{}
	for _, s := range scene.Effective.MCPServers {
		if s.OverriddenBy == "" {
			servers[s.Name] = s.Layer
		}
	}
	if len(servers) != 3 || servers["base"] != contextcap.LayerGlobal || servers["docs"] != contextcap.LayerOrg || servers["crm"] != contextcap.LayerScene {
		t.Fatalf("scene effective servers = %+v", scene.Effective.MCPServers)
	}
	connectors := map[string]string{}
	for _, c := range scene.Effective.Connectors {
		connectors[c.ID] = c.Layer
		if c.Name == "" {
			t.Fatalf("effective connector without a name: %+v", c)
		}
	}
	if len(connectors) != 3 || connectors[f.global] != contextcap.LayerGlobal || connectors[f.person] != contextcap.LayerOrg || connectors[f.scene] != contextcap.LayerScene {
		t.Fatalf("scene effective connectors = %+v", scene.Effective.Connectors)
	}
	skills := map[string]string{}
	for _, s := range scene.Effective.Skills {
		skills[s.ID] = s.Layer
	}
	if len(skills) != 2 || skills[f.skillAgent] != contextcap.LayerGlobal || skills[f.skillScene] != contextcap.LayerScene {
		t.Fatalf("scene effective skills = %+v", scene.Effective.Skills)
	}

	// The org config of a deleted tenant goes with it; a created tenant has
	// its own nodes.
	w := scenesAs(t, router, "", http.MethodPost, "/api/agents/"+agentID+"/tenants", map[string]string{"org_id": "org-ctxcap-b", "name": "Beta"})
	ctxcapExpectStatus(t, w, http.StatusCreated, "create tenant")
	betaOrg := ctxNodePath(agentID, "org-ctxcap-b", contextcap.ScopeOrg, "org-ctxcap-b")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, betaOrg+"/prompts", map[string]any{"prompts": []map[string]any{{"name": "tone", "text": "beta"}}}),
		http.StatusOK, "beta prompts")
	w = scenesAs(t, router, "", http.MethodGet, betaOrg, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "beta org node")
	var beta agentContextNodeResponse
	ctxcapDecode(t, w, &beta)
	if beta.Scope == nil || beta.Scope.Title != "Beta" || len(beta.Prompts) != 1 || beta.Prompts[0].Text != "beta" {
		t.Fatalf("beta org node = %+v", beta)
	}
	// The group of the identity org is not a scene of the beta tenant.
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodGet, ctxNodePath(agentID, "org-ctxcap-b", contextcap.ScopeScene, ctxcapScene), nil), http.StatusNotFound, "scene of another tenant")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodDelete, "/api/agents/"+agentID+"/tenants/org-ctxcap-b", nil), http.StatusNoContent, "delete beta")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodGet, betaOrg, nil), http.StatusNotFound, "node of a deleted tenant")
	if prompts, err := contextcap.ListPromptComponents(context.Background(), testPool, testWorkspaceID, agentID, contextcap.ScopeOrg, "org-ctxcap-b", "org-ctxcap-b"); err != nil || len(prompts) != 0 {
		t.Fatalf("beta org prompts after delete = %+v %v", prompts, err)
	}
}

// A manager connects an official app account for a created tenant's
// enterprise level from the Context Builder: the state carries the tenant
// org, the callback stores the credential in the org scope, switches the
// app on there and returns to the node; a deleted tenant takes its
// credential with it and can no longer be connected.
func TestAgentContextNodeConnectsOrgAccount(t *testing.T) {
	f := newCatalogFixture(t)
	f.cleanupAppScenes(t)
	const tenantOrg = "org-catalog-node"
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_tenant WHERE agent_id = $1`, f.agentID)
	})
	r := chi.NewRouter()
	r.Get(ConnectorOAuthCallbackPath, f.h.ConnectorOAuthCallback)
	r.With(RequireHumanActor).Post("/api/agents/{id}/tenants/{orgId}/context/{scopeType}/{scopeKey}/connections/start", f.h.StartAgentContextConnection)
	ctx := context.Background()
	dcr := f.create(t, f.dcr)
	f.offer(t, dcr.ID)
	if _, err := contextcap.CreateTenant(ctx, testPool, contextcap.TenantWrite{
		WorkspaceID: testWorkspaceID, AgentID: f.agentID, OrgID: tenantOrg, Name: "Beta", ActorID: testUserID,
	}); err != nil {
		t.Fatal(err)
	}
	startPath := "/api/agents/" + f.agentID + "/tenants/" + tenantOrg + "/context/org/" + tenantOrg + "/connections/start"
	returnTo := "/ws/agents/" + f.agentID + "?view=scenes&tenant=" + tenantOrg
	member := createPermissionTestMember(t, "node-connect-"+uuid.NewString()[:8]+"@example.test")
	ctxcapExpectStatus(t, catalogAdmin(t, r, http.MethodPost, startPath, member, map[string]string{"connector_id": dcr.ID}),
		http.StatusForbidden, "plain member, org connect")

	query, cookies := f.takeAuthorizeURL(t, catalogAdmin(t, r, http.MethodPost, startPath, testUserID,
		map[string]string{"connector_id": dcr.ID, "return_to": returnTo}))
	var stateType, stateOrg, stateKey string
	if err := testPool.QueryRow(ctx, `SELECT scope_type, org_id, scope_key FROM connector_oauth_state
		WHERE agent_id = $1 ORDER BY created_at DESC LIMIT 1`, f.agentID).Scan(&stateType, &stateOrg, &stateKey); err != nil ||
		stateType != contextcap.ScopeOrg || stateOrg != tenantOrg || stateKey != tenantOrg {
		t.Fatalf("org connect state = %s/%s/%s err=%v", stateType, stateOrg, stateKey, err)
	}
	rec := browserGet(r, ConnectorOAuthCallbackPath+"?code=good-code&state="+url.QueryEscape(query.Get("state")), cookies...)
	location, err := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || err != nil || location.Scheme+"://"+location.Host != catalogAppOrigin ||
		location.Path != "/ws/agents/"+f.agentID || location.Query().Get("view") != "scenes" ||
		location.Query().Get("tenant") != tenantOrg || location.Query().Get("connected") != f.dcr.Slug {
		t.Fatalf("org callback: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	key := contextcap.CredentialBinding{WorkspaceID: testWorkspaceID, AgentID: f.agentID, ConnectorID: dcr.ID,
		ScopeType: contextcap.ScopeOrg, OrgID: tenantOrg, ScopeKey: tenantOrg}
	if _, err := contextcap.GetCredential(ctx, testPool, key); err != nil {
		t.Fatalf("org credential after the callback: %v", err)
	}
	bindings, err := contextcap.ListScopeBindings(ctx, testPool, testWorkspaceID, f.agentID, contextcap.ScopeOrg, tenantOrg, tenantOrg)
	if err != nil || len(bindings) != 1 || bindings[0].ResourceID != dcr.ID || !bindings[0].Enabled {
		t.Fatalf("org binding after the callback = %+v err=%v", bindings, err)
	}

	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := contextcap.DeleteTenant(ctx, tx, testWorkspaceID, f.agentID, tenantOrg); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := contextcap.GetCredential(ctx, testPool, key); err == nil {
		t.Fatal("org credential survived the tenant delete")
	}
	rec = catalogAdmin(t, r, http.MethodPost, startPath, testUserID, map[string]string{"connector_id": dcr.ID})
	if rec.Code != http.StatusNotFound || catalogErrorCode(t, rec) != contextCapErrTenantNotFound {
		t.Fatalf("connect for a deleted tenant: %d %s", rec.Code, rec.Body.String())
	}
}

// A manager revokes the configure-page access granted on a group or a
// person: a person whose forwarded link handed their scope to someone else
// can then redeem a new personal link; the scope's configuration stays.
func TestAgentContextNodeRevokesGrants(t *testing.T) {
	f := newCtxcapFixture(t)
	f.cleanupScenes(t)
	f.cleanupGrantsAndLinks(t)
	router := scenesRouter(f.h)
	agentID := uuidToString(f.agent)
	ctx := context.Background()
	const (
		victimStaff = "staff-context-node-victim"
		victimDM    = "cidContextNodeVictimDM=="
	)
	mallory, alice := uuid.NewString(), uuid.NewString()

	// Mallory redeemed the victim's forwarded personal link (minted in the
	// victim's 1:1 chat): the person grant and the 1:1 chat grant.
	token, err := contextcap.NewLinkToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contextcap.InsertLink(ctx, testPool, contextcap.Link{
		TokenHash: contextcap.HashLinkToken(token), WorkspaceID: testWorkspaceID, AgentID: agentID, ScopeType: contextcap.ScopePerson,
		OrgID: ctxcapOrg, ScopeKey: victimStaff, ScopeTitle: "Victim", ExtraSceneKey: victimDM,
	}, contextcap.LinkTTL(contextcap.ScopePerson)); err != nil {
		t.Fatal(err)
	}
	if _, err := contextcap.RedeemLink(ctx, testPool, contextcap.HashLinkToken(token), mallory); err != nil {
		t.Fatal(err)
	}
	for _, g := range []contextcap.Grant{
		{UserID: mallory, ScopeType: contextcap.ScopePerson, ScopeKey: victimStaff, ScopeTitle: "Victim"},
		{UserID: mallory, ScopeType: contextcap.ScopeScene, ScopeKey: victimDM},
		{UserID: alice, ScopeType: contextcap.ScopeScene, ScopeKey: ctxcapScene},
	} {
		g.WorkspaceID, g.AgentID, g.OrgID, g.Source = testWorkspaceID, agentID, ctxcapOrg, contextcap.GrantSourceAgentLink
		if _, err := contextcap.UpsertGrant(ctx, testPool, g, contextcap.GrantTTL(g.ScopeType)); err != nil {
			t.Fatal(err)
		}
	}
	held := func() bool {
		t.Helper()
		tx, err := testPool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		held, err := contextcap.PersonHeldByOther(ctx, tx, uuid.NewString(), agentID, ctxcapOrg, victimStaff)
		if err != nil {
			t.Fatal(err)
		}
		return held
	}
	if !held() {
		t.Fatal("fixture: the victim's scope must be held by another account")
	}
	liveGrants := func(userID string) int {
		t.Helper()
		grants, err := contextcap.ListLiveGrantsForUser(ctx, testPool, userID, agentID)
		if err != nil {
			t.Fatal(err)
		}
		return len(grants)
	}

	personGrants := ctxNodePath(agentID, ctxcapOrg, contextcap.ScopePerson, victimStaff) + "/grants"
	member := createPermissionTestMember(t, "context-node-revoke-"+uuid.NewString()[:8]+"@example.test")
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE user_id = $1`, member)
	})
	ctxcapExpectStatus(t, scenesAs(t, router, member, http.MethodDelete, personGrants, nil), http.StatusForbidden, "plain member revoke")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodDelete, ctxNodePath(agentID, ctxcapOrg, contextcap.ScopeOrg, ctxcapOrg)+"/grants", nil),
		http.StatusBadRequest, "org level has no grants")

	w := scenesAs(t, router, "", http.MethodDelete, personGrants, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "manager revokes the person")
	if strings.TrimSpace(w.Body.String()) != `{"revoked":2}` {
		t.Fatalf("person revoke = %s", w.Body.String())
	}
	if held() || liveGrants(mallory) != 0 {
		t.Fatalf("after the person revoke: held=%v mallory grants=%d", held(), liveGrants(mallory))
	}
	if liveGrants(alice) != 1 {
		t.Fatal("a person revoke must not touch other scopes")
	}

	w = scenesAs(t, router, "", http.MethodDelete, ctxNodePath(agentID, ctxcapOrg, contextcap.ScopeScene, ctxcapScene)+"/grants", nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "manager revokes the group")
	if strings.TrimSpace(w.Body.String()) != `{"revoked":1}` || liveGrants(alice) != 0 {
		t.Fatalf("group revoke = %s, alice grants=%d", w.Body.String(), liveGrants(alice))
	}
}
