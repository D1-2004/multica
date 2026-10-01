package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Claim-time Context Builder (docs/context-capabilities.md §3): the org,
// scene and person layers of a task's tenant org are applied at claim (skills,
// prompt components, custom MCP servers, connectors) and on every relay call.

const (
	ctxBuilderAgentInstructions = "Agent base instructions."
	ctxBuilderAgentMCPConfig    = `{"mcpServers":{"docs":{"url":"https://agent.example.test"},"base":{"command":"agent-base"}}}`
	ctxBuilderOtherOrg          = "org-ctxcap-other"
)

// ctxBuilder is a ctxcap fixture whose agent has instructions, its own MCP
// servers, an extra offered connector and an extra offered skill for the org
// level, and every connector and both skills offered.
type ctxBuilder struct {
	*ctxcapFixture
	router   http.Handler
	agentID  string
	orgConn  string // offered bearer connector without a workspace credential
	orgSkill string // offered skill
	// personUser holds the fixture person's grant once putPerson ran.
	personUser string
}

func newCtxBuilder(t *testing.T) *ctxBuilder {
	t.Helper()
	f := newCtxcapFixture(t)
	b := &ctxBuilder{ctxcapFixture: f, router: scenesRouter(f.h), agentID: uuidToString(f.agent)}
	b.orgConn = f.insertConnector(t, "bearer", "")
	b.orgSkill = f.insertSkill(t, "ctxcap-org-skill")
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = testPool.Exec(bg, `DELETE FROM internal_connector WHERE id = $1`, b.orgConn)
		_, _ = testPool.Exec(bg, `DELETE FROM skill WHERE id = $1`, b.orgSkill)
	})
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE agent SET instructions = $2, mcp_config = $3 WHERE id = $1`,
		b.agentID, ctxBuilderAgentInstructions, ctxBuilderAgentMCPConfig); err != nil {
		t.Fatal(err)
	}
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := contextcap.ReplaceOffers(ctx, tx, testWorkspaceID, b.agentID,
		[]string{f.scene, f.person, f.global, b.orgConn}, []string{f.skillScene, b.orgSkill}, testUserID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return b
}

// put writes one Context Builder node of orgID through the admin API.
func (b *ctxBuilder) put(t *testing.T, orgID, scopeType, key, what string, body any) {
	t.Helper()
	ctxcapExpectStatus(t, scenesAs(t, b.router, "", http.MethodPut, ctxNodePath(b.agentID, orgID, scopeType, key)+"/"+what, body), http.StatusOK,
		scopeType+" "+what)
}

func ctxBuilderPrompts(prompts ...[3]any) map[string]any {
	list := []map[string]any{}
	for _, p := range prompts {
		list = append(list, map[string]any{"name": p[0], "order": p[1], "text": p[2]})
	}
	return map[string]any{"prompts": list}
}

func ctxBuilderServers(servers map[string]string) map[string]any {
	out := map[string]any{}
	for name, url := range servers {
		out[name] = map[string]any{"type": "http", "url": url}
	}
	return map[string]any{"mcp_config": map[string]any{"mcpServers": out}}
}

// configureIdentityTenant gives the identity tenant's org, the fixture group
// and the fixture person their layers:
//
//   - prompts: org tone(1) + rules(2), group tone(1), person me(3);
//   - custom MCP servers: org docs + crm, group crm + a reserved "multica",
//     person notes;
//   - org bindings: orgConn and orgSkill (the fixture already binds the
//     scene connector and skill to the group and the no-auth connector to
//     the person);
//   - org credentials: orgConn, the scene connector and the global one.
//
// The person layer is the person's own (contextCapScopeRights): it is
// written on the configure page by the holder of the person grant, not by
// the manager.
func (b *ctxBuilder) configureIdentityTenant(t *testing.T) {
	t.Helper()
	b.put(t, ctxcapOrg, contextcap.ScopeOrg, ctxcapOrg, "prompts", ctxBuilderPrompts([3]any{"tone", 1, "org tone"}, [3]any{"rules", 2, "org rules"}))
	b.put(t, ctxcapOrg, contextcap.ScopeOrg, ctxcapOrg, "bindings", map[string]any{"resource_type": "connector", "resource_id": b.orgConn, "enabled": true})
	b.put(t, ctxcapOrg, contextcap.ScopeOrg, ctxcapOrg, "bindings", map[string]any{"resource_type": "skill", "resource_id": b.orgSkill, "enabled": true})
	b.put(t, ctxcapOrg, contextcap.ScopeOrg, ctxcapOrg, "mcp-config", ctxBuilderServers(map[string]string{
		"docs": "https://org-docs.example.test", "crm": "https://org-crm.example.test",
	}))
	for connector, bearer := range map[string]string{b.orgConn: "org-conn-secret", b.scene: "org-scene-secret", b.global: "org-global-secret"} {
		b.put(t, ctxcapOrg, contextcap.ScopeOrg, ctxcapOrg, "credentials", map[string]string{"connector_id": connector, "bearer": bearer})
	}
	b.put(t, ctxcapOrg, contextcap.ScopeScene, ctxcapScene, "prompts", ctxBuilderPrompts([3]any{"tone", 1, "group tone"}))
	b.put(t, ctxcapOrg, contextcap.ScopeScene, ctxcapScene, "mcp-config", ctxBuilderServers(map[string]string{
		"crm": "https://group-crm.example.test", "multica": "https://hijack.example.test",
	}))
	b.putPerson(t, "prompts", ctxBuilderPrompts([3]any{"me", 3, "person note"}))
	b.putPerson(t, "mcp-config", ctxBuilderServers(map[string]string{"notes": "https://person-notes.example.test"}))
}

// putPerson writes the fixture person's scope through the configure page as
// that person (a live person grant).
func (b *ctxBuilder) putPerson(t *testing.T, what string, body map[string]any) {
	t.Helper()
	if b.personUser == "" {
		b.personUser = uuid.NewString()
		b.grant(t, b.personUser, contextcap.ScopePerson, ctxcapStaff, "Ctxcap person")
	}
	payload := map[string]any{"scope_type": contextcap.ScopePerson, "scope_key": ctxcapStaff}
	for key, value := range body {
		payload[key] = value
	}
	ctxcapExpectStatus(t, ctxcapMobile(t, ctxcapRouter(b.h), http.MethodPut, "/api/context-capabilities/agents/"+b.agentID+"/"+what, b.personUser, payload),
		http.StatusOK, "person "+what)
}

// claim builds the claim payload of task as the daemon claim does.
func (b *ctxBuilder) claim(t *testing.T, task db.AgentTaskQueue) *TaskAgentData {
	t.Helper()
	ctx := context.Background()
	stored, err := testHandler.Queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(testRuntimeID))
	if err != nil {
		t.Fatal(err)
	}
	req := newDaemonTokenRequest(http.MethodPost, "/claim", nil, testWorkspaceID, "ctxcap-claim")
	resp, _, _, _, failure := testHandler.buildClaimedTaskResponse(req, &stored, runtime, "", testRuntimeID, testWorkspaceID)
	if failure != nil {
		t.Fatalf("claim failed: %+v", failure)
	}
	if resp.Agent == nil {
		t.Fatal("claim carries no agent data")
	}
	return resp.Agent
}

func ctxBuilderJSON(t *testing.T, agent *TaskAgentData) string {
	t.Helper()
	raw, err := json.Marshal(agent)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// ctxBuilderMCPServers decodes the mcpServers of a claim to name -> url (or
// command).
func ctxBuilderMCPServers(t *testing.T, config json.RawMessage) map[string]string {
	t.Helper()
	var document struct {
		MCPServers map[string]struct {
			URL     string `json:"url"`
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(config, &document); err != nil {
		t.Fatalf("claim mcp_config %s: %v", config, err)
	}
	out := map[string]string{}
	for name, server := range document.MCPServers {
		out[name] = server.URL + server.Command
	}
	return out
}

func ctxBuilderSkillIDs(agent *TaskAgentData) map[string]bool {
	out := map[string]bool{}
	for _, skill := range agent.Skills {
		out[skill.ID] = true
	}
	return out
}

func ctxBuilderGroupTask(sender string, messageSenders ...string) []byte {
	return ctxcapWithDispatchOrg(ctxcapDispatch("group", ctxcapScene, sender, messageSenders...), ctxcapOrg)
}

// A task in a tenant org gets the org, group and person layers: prompt
// components and custom MCP servers merge by name (nearest wins), skills and
// connectors are a union, credentials go person > scene > org > workspace,
// and the admin preview shows what the runtime applies.
func TestContextBuilderClaimAppliesTenantLayers(t *testing.T) {
	b := newCtxBuilder(t)
	b.configureIdentityTenant(t)
	b.setCredential(t, b.scene, contextcap.ScopeScene, ctxcapScene, "scene-connector-secret")
	b.setCredential(t, b.global, contextcap.ScopePerson, ctxcapStaff, "person-global-secret")
	task := b.task(t, ctxBuilderGroupTask(ctxcapStaff, ctxcapStaff))

	agent := b.claim(t, task)
	wantBlock := contextcap.ContextPromptHeading +
		"\n\n### tone\n\ngroup tone" +
		"\n\n### rules\n\norg rules" +
		"\n\n### me\n\nperson note"
	if !strings.HasPrefix(agent.Instructions, ctxBuilderAgentInstructions+"\n\n") || !strings.HasSuffix(agent.Instructions, "\n\n"+wantBlock) ||
		strings.Count(agent.Instructions, contextcap.ContextPromptHeading) != 1 || strings.Contains(agent.Instructions, "org tone") {
		t.Fatalf("claim instructions = %q, want the agent's followed by %q", agent.Instructions, wantBlock)
	}
	servers := ctxBuilderMCPServers(t, agent.McpConfig)
	wantServers := map[string]string{
		"base":  "agent-base",                        // the agent's own
		"docs":  "https://org-docs.example.test",     // org replaces the agent's
		"crm":   "https://group-crm.example.test",    // group replaces org
		"notes": "https://person-notes.example.test", // person
	}
	if len(servers) != len(wantServers) {
		t.Fatalf("claim MCP servers = %v, want %v", servers, wantServers)
	}
	for name, want := range wantServers {
		if servers[name] != want {
			t.Fatalf("claim MCP server %s = %q, want %q (all: %v)", name, servers[name], want, servers)
		}
	}
	skills := ctxBuilderSkillIDs(agent)
	if !skills[b.skillAgent] || !skills[b.skillScene] || !skills[b.orgSkill] || skills[b.skillFree] {
		t.Fatalf("claim skills = %v", skills)
	}

	// Connectors at claim and per relay call: union, credential precedence.
	got := b.resolve(t, task)
	want := map[string]ctxcapResolved{
		b.global:  {connectorBindingGlobal, connectorCredentialPerson, "person-global-secret"},
		b.scene:   {connectorBindingScene, connectorCredentialScene, "scene-connector-secret"},
		b.person:  {connectorBindingPerson, connectorCredentialNone, ""},
		b.orgConn: {connectorBindingOrg, connectorCredentialOrg, "org-conn-secret"},
	}
	if len(got) != len(want) {
		t.Fatalf("resolved connectors = %#v", got)
	}
	for id, w := range want {
		if got[id] != w {
			t.Fatalf("connector %s = %#v, want %#v", id, got[id], w)
		}
	}
	runtime := db.AgentRuntime{WorkspaceID: b.ws, RuntimeMode: "cloud", Metadata: []byte(`{"kind":"fc-e2b","provider":"opencode"}`)}
	data := &TaskAgentData{McpConfig: agent.McpConfig}
	if err := b.h.injectRunnerMCP(context.Background(), runtime, task, "task-token", data, true, true); err != nil {
		t.Fatalf("managed MCP injection over the context servers: %v", err)
	}
	if _, ok := data.McpRelayRoutes[connectorServerName(b.orgConn)]; !ok {
		t.Fatalf("org connector not mounted: %#v", data.McpRelayRoutes)
	}
	if servers := ctxBuilderMCPServers(t, data.McpConfig); servers["notes"] == "" || !strings.HasSuffix(servers["multica"], "/api/mcp") {
		t.Fatalf("injected MCP servers = %v", servers)
	}

	// Fall through the credential layers of the same running task.
	for _, step := range []struct {
		connector, scopeType, key string
		want                      ctxcapResolved
	}{
		{b.global, contextcap.ScopePerson, ctxcapStaff, ctxcapResolved{connectorBindingGlobal, connectorCredentialOrg, "org-global-secret"}},
		{b.global, contextcap.ScopeOrg, ctxcapOrg, ctxcapResolved{connectorBindingGlobal, connectorCredentialWorkspace, "workspace-secret"}},
		{b.scene, contextcap.ScopeScene, ctxcapScene, ctxcapResolved{connectorBindingScene, connectorCredentialOrg, "org-scene-secret"}},
	} {
		if _, err := contextcap.DeleteCredential(context.Background(), testPool, contextcap.CredentialBinding{
			WorkspaceID: testWorkspaceID, AgentID: b.agentID, ConnectorID: step.connector, ScopeType: step.scopeType, OrgID: ctxcapOrg, ScopeKey: step.key,
		}); err != nil {
			t.Fatal(err)
		}
		if got := b.resolve(t, task)[step.connector]; got != step.want {
			t.Fatalf("after removing the %s credential, connector %s = %#v, want %#v", step.scopeType, step.connector, got, step.want)
		}
	}

	// The relay re-resolves the org layer on every call.
	if rec := b.relay(t, task, b.orgConn); rec.Code != http.StatusOK {
		t.Fatalf("relay to the org connector: %d %s", rec.Code, rec.Body.String())
	}
	if got := b.upstream.last("/" + b.orgConn); got != "Bearer org-conn-secret" {
		t.Fatalf("org connector sent %q", got)
	}
	var bindingLayer, credentialLayer string
	if err := testPool.QueryRow(context.Background(), `SELECT binding_layer, credential_layer FROM internal_connector_call_audit
		WHERE task_id = $1 AND connector_id = $2 AND outcome = 'ok'`, uuidToString(task.ID), b.orgConn).Scan(&bindingLayer, &credentialLayer); err != nil {
		t.Fatal(err)
	}
	if bindingLayer != connectorBindingOrg || credentialLayer != connectorCredentialOrg {
		t.Fatalf("audit layers = %s/%s", bindingLayer, credentialLayer)
	}
	b.put(t, ctxcapOrg, contextcap.ScopeOrg, ctxcapOrg, "bindings", map[string]any{"resource_type": "connector", "resource_id": b.orgConn, "enabled": false})
	if rec := b.relay(t, task, b.orgConn); rec.Code != http.StatusForbidden {
		t.Fatalf("relay after the org switch-off: %d %s", rec.Code, rec.Body.String())
	}
}

// The admin 生效预览 of a group node and a run of that group (no single
// sender, so no person layer) come from the same merge.
func TestContextBuilderPreviewMatchesRuntime(t *testing.T) {
	b := newCtxBuilder(t)
	b.configureIdentityTenant(t)
	task := b.task(t, ctxBuilderGroupTask(ctxcapStaff, ctxcapStaff, ctxcapOtherStaff))
	runtime, err := b.h.taskEffectiveContext(context.Background(), b.ws, task, contextcap.ContextLayer{Layer: contextcap.LayerGlobal, MCPConfig: json.RawMessage(ctxBuilderAgentMCPConfig)})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Scope.OrgID != ctxcapOrg || !runtime.Scope.HasOrg() || runtime.Scope.SceneKey != ctxcapScene || runtime.Scope.HasPerson() || len(runtime.Layers) != 2 {
		t.Fatalf("runtime scope = %#v layers=%d", runtime.Scope, len(runtime.Layers))
	}
	// A group server named like a managed server is left out of both.
	if len(runtime.ReservedMCPServers) != 1 || runtime.ReservedMCPServers[0] != contextcap.LayerScene+":multica" {
		t.Fatalf("reserved servers = %v", runtime.ReservedMCPServers)
	}
	node := ctxNode(t, b.router, "", b.agentID, contextcap.ScopeScene, ctxcapScene)
	if len(node.Effective.Prompts) != len(runtime.Effective.Prompts) || len(node.Effective.MCPServers) != len(runtime.Effective.MCPServers) {
		t.Fatalf("preview %+v vs runtime %+v", node.Effective, runtime.Effective)
	}
	for i, p := range runtime.Effective.Prompts {
		if got := node.Effective.Prompts[i]; got.Name != p.Name || got.Text != p.Text || got.Layer != p.Layer || got.OverriddenBy != p.OverriddenBy {
			t.Fatalf("prompt %d: preview %+v, runtime %+v", i, got, p)
		}
	}
	for i, s := range runtime.Effective.MCPServers {
		if got := node.Effective.MCPServers[i]; got.Name != s.Name || got.Layer != s.Layer || got.OverriddenBy != s.OverriddenBy {
			t.Fatalf("MCP server %d: preview %+v, runtime %+v", i, got, s)
		}
	}
	runtimeSkills := map[string]bool{}
	for _, id := range runtime.contextSkillIDs() {
		runtimeSkills[uuidToString(id)] = true
	}
	for _, s := range node.Effective.Skills {
		if s.Layer != contextcap.LayerGlobal && !runtimeSkills[s.ID] {
			t.Fatalf("preview skill %+v missing at runtime %v", s, runtimeSkills)
		}
	}
	if len(runtimeSkills) != 2 || !runtimeSkills[b.orgSkill] || !runtimeSkills[b.skillScene] {
		t.Fatalf("runtime context skills = %v", runtimeSkills)
	}
}

// A dispatched task with neither a group nor a single sender still gets the
// org layer of its tenant.
func TestContextBuilderOrgLayerWithoutSceneOrPerson(t *testing.T) {
	b := newCtxBuilder(t)
	b.configureIdentityTenant(t)
	raw := ctxcapWithDispatchOrg([]byte(`{"dispatch_event_data":{"conversation":{"type":"group","openConversationId":"not-a-cid"},"sender":{}}}`), ctxcapOrg)
	task := b.task(t, raw)
	agent := b.claim(t, task)
	wantBlock := contextcap.ContextPromptHeading + "\n\n### tone\n\norg tone\n\n### rules\n\norg rules"
	if !strings.HasSuffix(agent.Instructions, "\n\n"+wantBlock) {
		t.Fatalf("org-only instructions = %q", agent.Instructions)
	}
	if servers := ctxBuilderMCPServers(t, agent.McpConfig); servers["crm"] != "https://org-crm.example.test" || servers["notes"] != "" {
		t.Fatalf("org-only MCP servers = %v", servers)
	}
	got := b.resolve(t, task)
	if got[b.orgConn] != (ctxcapResolved{connectorBindingOrg, connectorCredentialOrg, "org-conn-secret"}) ||
		got[b.global] != (ctxcapResolved{connectorBindingGlobal, connectorCredentialOrg, "org-global-secret"}) || len(got) != 2 {
		t.Fatalf("org-only connectors = %#v", got)
	}
}

// Tasks without a scope (no dispatch context, a manual rerun, an org that is
// not a tenant) get a claim that is byte for byte the one they got before
// any context layer existed. Creating a tenant for the org turns its layers
// on; deleting it turns them off again.
func TestContextBuilderTasksWithoutScopeAreUnchanged(t *testing.T) {
	b := newCtxBuilder(t)
	ctx := context.Background()
	group := ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff)
	a2a, _ := json.Marshal(map[string]any{
		"multica_origin":      "a2a",
		"external_identity":   map[string]any{"dws": map[string]string{"orgId": ctxcapOrg}},
		"dispatch_event_data": map[string]any{"conversation": map[string]any{"openConversationId": ctxcapScene, "type": "group"}, "sender": map[string]any{"staffId": ctxcapStaff}},
	})
	tasks := map[string]db.AgentTaskQueue{
		"A2A":                 b.task(t, a2a),
		"no dispatch context": b.task(t, []byte(`{"issue_id":"x"}`)),
		"manual rerun":        b.rerunTask(t, ctxcapReplayed(ctxcapWithDispatchOrg(group, ctxcapOrg))),
		"rerun lineage":       b.rerunTask(t, ctxcapWithDispatchOrg(group, ctxcapOrg)),
		"org not a tenant":    b.task(t, ctxcapWithDispatchOrg(group, ctxBuilderOtherOrg)),
	}
	baseline := map[string]string{}
	for name, task := range tasks {
		agent := b.claim(t, task)
		if agent.Instructions != ctxBuilderAgentInstructions || string(agent.McpConfig) != ctxBuilderStoredMCPConfig(t, b.agentID) {
			t.Fatalf("%s: baseline claim = %q / %s", name, agent.Instructions, agent.McpConfig)
		}
		baseline[name] = ctxBuilderJSON(t, agent)
	}

	// Every layer of the identity tenant, plus configuration stored under
	// the other org (as a deleted tenant leaves its groups and people).
	b.configureIdentityTenant(t)
	for _, scope := range []struct{ scopeType, key string }{
		{contextcap.ScopeOrg, ctxBuilderOtherOrg}, {contextcap.ScopeScene, ctxcapScene}, {contextcap.ScopePerson, ctxcapStaff},
	} {
		if _, err := contextcap.ReplacePromptComponents(ctx, testPool, contextcap.PromptComponentsWrite{
			WorkspaceID: testWorkspaceID, AgentID: b.agentID, ScopeType: scope.scopeType, OrgID: ctxBuilderOtherOrg, ScopeKey: scope.key,
			Components: []contextcap.PromptComponentInput{{Name: "other", Order: 0, Text: "other " + scope.scopeType}},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := contextcap.PutScopeMCPConfig(ctx, testPool, contextcap.ScopeMCPConfigWrite{
			WorkspaceID: testWorkspaceID, AgentID: b.agentID, ScopeType: scope.scopeType, OrgID: ctxBuilderOtherOrg, ScopeKey: scope.key,
			MCPConfig: json.RawMessage(`{"mcpServers":{"other-` + scope.scopeType + `":{"url":"https://other.example.test"}}}`),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := contextcap.UpsertBinding(ctx, testPool, contextcap.BindingWrite{
			WorkspaceID: testWorkspaceID, AgentID: b.agentID, ScopeType: scope.scopeType, OrgID: ctxBuilderOtherOrg, ScopeKey: scope.key,
			ResourceType: contextcap.ResourceSkill, ResourceID: b.orgSkill, Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	otherCredential := contextcap.CredentialBinding{WorkspaceID: testWorkspaceID, AgentID: b.agentID, ConnectorID: b.global,
		ScopeType: contextcap.ScopePerson, OrgID: ctxBuilderOtherOrg, ScopeKey: ctxcapStaff}
	sealed, err := contextcap.SealCredential(b.box, otherCredential, "other-org-person-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contextcap.UpsertCredential(ctx, testPool, otherCredential, sealed, contextcap.Hint("other-org-person-secret"), testUserID); err != nil {
		t.Fatal(err)
	}

	unchanged := func(stage string) {
		t.Helper()
		for name, task := range tasks {
			if got := ctxBuilderJSON(t, b.claim(t, task)); got != baseline[name] {
				t.Fatalf("%s, %s: claim changed\nbefore %s\nafter  %s", stage, name, baseline[name], got)
			}
			got := b.resolve(t, task)
			if len(got) != 1 || got[b.global] != (ctxcapResolved{connectorBindingGlobal, connectorCredentialWorkspace, "workspace-secret"}) {
				t.Fatalf("%s, %s: connectors = %#v", stage, name, got)
			}
		}
	}
	unchanged("with layers configured")

	// A tenant for the other org turns its layers on for its tasks.
	ctxcapExpectStatus(t, scenesAs(t, b.router, "", http.MethodPost, "/api/agents/"+b.agentID+"/tenants",
		map[string]string{"org_id": ctxBuilderOtherOrg, "name": "Other"}), http.StatusCreated, "create tenant")
	other := b.claim(t, tasks["org not a tenant"])
	if !strings.HasSuffix(other.Instructions, "\n\n"+contextcap.ContextPromptHeading+"\n\n### other\n\nother person") ||
		strings.Contains(other.Instructions, "group tone") {
		t.Fatalf("tenant claim instructions = %q", other.Instructions)
	}
	if servers := ctxBuilderMCPServers(t, other.McpConfig); servers["other-org"] == "" || servers["other-scene"] == "" || servers["other-person"] == "" || servers["crm"] != "" {
		t.Fatalf("tenant claim MCP servers = %v", servers)
	}
	if !ctxBuilderSkillIDs(other)[b.orgSkill] {
		t.Fatalf("tenant claim skills = %v", ctxBuilderSkillIDs(other))
	}
	if got := b.resolve(t, tasks["org not a tenant"])[b.global]; got != (ctxcapResolved{connectorBindingGlobal, connectorCredentialPerson, "other-org-person-secret"}) {
		t.Fatalf("tenant task global connector = %#v", got)
	}

	// Deleting the tenant removes its org configuration; its groups and
	// people keep theirs but no longer apply.
	ctxcapExpectStatus(t, scenesAs(t, b.router, "", http.MethodDelete, "/api/agents/"+b.agentID+"/tenants/"+ctxBuilderOtherOrg, nil),
		http.StatusNoContent, "delete tenant")
	unchanged("after the tenant was deleted")
}

// ctxBuilderStoredMCPConfig is the agent's mcp_config as the database
// returns it (what a claim without context layers forwards).
func ctxBuilderStoredMCPConfig(t *testing.T, agentID string) string {
	t.Helper()
	var raw []byte
	if err := testPool.QueryRow(context.Background(), `SELECT mcp_config FROM agent WHERE id = $1`, agentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// An agent without a DingTalk identity (a robot channel) keeps its scene and
// personal configuration under org "", where its configure page and links
// write it: a task without a recorded dispatch org gets those layers (no
// org layer), and the Coordinator still mints its configuration links.
func TestContextBuilderAgentWithoutIdentityUsesOrglessLayers(t *testing.T) {
	b := newCtxBuilder(t)
	b.cleanupGrantsAndLinks(t)
	b.h.cfg.AppURL = "https://app.multica.example"
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `DELETE FROM agent_dingtalk_identity WHERE agent_id = $1`, b.agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := contextcap.ReplacePromptComponents(ctx, testPool, contextcap.PromptComponentsWrite{
		WorkspaceID: testWorkspaceID, AgentID: b.agentID, ScopeType: contextcap.ScopeScene, OrgID: "", ScopeKey: ctxcapScene,
		Components: []contextcap.PromptComponentInput{{Name: "tone", Order: 1, Text: "orgless group tone"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := contextcap.UpsertBinding(ctx, testPool, contextcap.BindingWrite{
		WorkspaceID: testWorkspaceID, AgentID: b.agentID, ScopeType: contextcap.ScopeScene, OrgID: "", ScopeKey: ctxcapScene,
		ResourceType: contextcap.ResourceSkill, ResourceID: b.orgSkill, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	task := b.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff))
	scope, skipped := b.h.resolveTaskContextScope(ctx, b.ws, task)
	if skipped != "" || scope.OrgID != "" || scope.HasOrg() || scope.SceneKey != ctxcapScene || scope.PersonKey != ctxcapStaff {
		t.Fatalf("orgless scope = %+v skipped=%q", scope, skipped)
	}
	agent := b.claim(t, task)
	if !strings.HasSuffix(agent.Instructions, "\n\n"+contextcap.ContextPromptHeading+"\n\n### tone\n\norgless group tone") {
		t.Fatalf("orgless claim instructions = %q", agent.Instructions)
	}
	if !ctxBuilderSkillIDs(agent)[b.orgSkill] {
		t.Fatalf("orgless claim skills = %v", ctxBuilderSkillIDs(agent))
	}
	// A dispatch recorded under an org that is not a created tenant still
	// gets nothing.
	foreign := b.task(t, ctxcapWithDispatchOrg(ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff), ctxBuilderOtherOrg))
	if _, skipped := b.h.resolveTaskContextScope(ctx, b.ws, foreign); skipped != taskContextNotTenant {
		t.Fatalf("foreign org skipped=%q", skipped)
	}

	link, err := NewCoordinatorConfigLinkIssuer(b.h).IssueConfigLink(ctx, inboundcoord.ConfigLinkRequest{
		WorkspaceID: testWorkspaceID, AgentID: b.agentID, DispatchContext: ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff),
	})
	if err != nil || link.Scope != contextcap.ScopeScene {
		t.Fatalf("orgless Coordinator link = %+v err=%v", link, err)
	}
	if stored := coordinatorStoredLinkFor(t, coordinatorLinkToken(t, link)); stored.orgID != "" || stored.scopeKey != ctxcapScene {
		t.Fatalf("orgless stored link = %+v", stored)
	}
}

// A custom MCP server of a context layer named like one of the agent's
// Runner MCP servers is left out at claim, so the Runner mount keeps its
// name and the claim does not fail on mcp_server_name_conflict.
func TestContextBuilderScopeServerYieldsToRunnerMount(t *testing.T) {
	b := newCtxBuilder(t)
	b.put(t, ctxcapOrg, contextcap.ScopeScene, ctxcapScene, "mcp-config", ctxBuilderServers(map[string]string{
		"github": "https://group-github.example.test", "crm": "https://group-crm.example.test",
	}))
	ctx := context.Background()
	machineID := uuid.NewString()
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = testPool.Exec(bg, `DELETE FROM agent_runner_binding WHERE agent_id = $1`, b.agentID)
		_, _ = testPool.Exec(bg, `DELETE FROM runner_mcp_config WHERE machine_id = $1`, machineID)
		_, _ = testPool.Exec(bg, `DELETE FROM runner_machine WHERE id = $1`, machineID)
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO runner_machine
		(id, owner_id, name, os, arch, public_key, client_version, last_seen_at, connection_id)
		VALUES ($1, $2, 'ctx-builder-runner', 'linux', 'amd64', $3, 'test', now(), $4)`,
		machineID, testUserID, []byte(strings.ReplaceAll(uuid.NewString(), "-", "")), uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_runner_binding
		(workspace_id, agent_id, machine_id, bound_by, roots, enabled_mcp_servers)
		VALUES ($1, $2, $3, $4, '[]'::jsonb, '{"github":"fingerprint"}'::jsonb)`,
		testWorkspaceID, b.agentID, machineID, testUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO runner_mcp_config (machine_id, config, revision) VALUES ($1, $2, 'rev-1')`,
		machineID, []byte(`{"mcpServers":{"github":{"type":"http","url":"https://runner-github.example.test"}}}`)); err != nil {
		t.Fatal(err)
	}

	task := b.task(t, ctxBuilderGroupTask(ctxcapStaff, ctxcapStaff, ctxcapOtherStaff))
	agent := b.claim(t, task)
	if servers := ctxBuilderMCPServers(t, agent.McpConfig); servers["github"] != "https://group-github.example.test" {
		t.Fatalf("claim MCP servers before the Runner mount = %v", servers)
	}
	runtime := db.AgentRuntime{WorkspaceID: b.ws, RuntimeMode: "cloud", Metadata: []byte(`{"kind":"fc-e2b","provider":"opencode"}`)}
	if err := b.h.injectRunnerMCP(ctx, runtime, task, "task-token", agent, true, true); err != nil {
		t.Fatalf("Runner MCP injection with a shadowing scope server: %v", err)
	}
	servers := ctxBuilderMCPServers(t, agent.McpConfig)
	if servers["github"] != "https://runner-github.example.test" || servers["crm"] != "https://group-crm.example.test" {
		t.Fatalf("injected MCP servers = %v", servers)
	}
	if route := agent.McpRelayRoutes["github"]; !strings.HasPrefix(route.Path, "/api/runner-mcp/mounts/") {
		t.Fatalf("Runner route = %#v", agent.McpRelayRoutes)
	}
}

// A Pi sandbox without the mcp extension runs a task of a scope with custom
// MCP servers without them instead of cancelling it.
func TestContextBuilderPiWithoutMCPSkipsScopeServers(t *testing.T) {
	b := newCtxBuilder(t)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE agent SET mcp_config = NULL WHERE id = $1`, b.agentID); err != nil {
		t.Fatal(err)
	}
	b.put(t, ctxcapOrg, contextcap.ScopeScene, ctxcapScene, "mcp-config", ctxBuilderServers(map[string]string{"crm": "https://group-crm.example.test"}))
	b.put(t, ctxcapOrg, contextcap.ScopeScene, ctxcapScene, "prompts", ctxBuilderPrompts([3]any{"tone", 1, "group tone"}))
	task := b.task(t, ctxBuilderGroupTask(ctxcapStaff, ctxcapStaff, ctxcapOtherStaff))
	stored, err := testHandler.Queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(testRuntimeID))
	if err != nil {
		t.Fatal(err)
	}
	runtime.RuntimeMode = "cloud"
	runtime.Provider = "pi"
	runtime.Metadata = []byte(`{"kind":"fc-e2b","capabilities":[]}`)
	req := newDaemonTokenRequest(http.MethodPost, "/claim", nil, testWorkspaceID, "ctxcap-claim")
	resp, _, _, _, failure := testHandler.buildClaimedTaskResponse(req, &stored, runtime, "", testRuntimeID, testWorkspaceID)
	if failure != nil {
		t.Fatalf("Pi claim failed: %+v", failure)
	}
	if resp.Agent == nil || len(resp.Agent.McpConfig) != 0 || len(resp.Agent.contextMCPServers) != 0 {
		t.Fatalf("Pi claim agent = %+v", resp.Agent)
	}
	if !strings.Contains(resp.Agent.Instructions, "group tone") {
		t.Fatalf("Pi claim instructions = %q", resp.Agent.Instructions)
	}
}
