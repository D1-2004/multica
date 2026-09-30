package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/contextcap"
)

const (
	sceneConfigDirect      = "cidSceneConfigDirect=="
	sceneConfigDirectStaff = "staff-scene-config-dora"
)

// scenesAdminAs issues an admin scene request as userID, from a DingTalk
// sign-in when dingTalk is set (the configure page's credential routes need
// one, so can_connect depends on it).
func scenesAdminAs(t *testing.T, router http.Handler, userID, method, path string, body any, dingTalk bool) *httptest.ResponseRecorder {
	t.Helper()
	req := newRequest(method, path, body)
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	if dingTalk {
		req.Header.Set("X-Auth-Method", "dingtalk")
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func sceneConfigDetail(t *testing.T, router http.Handler, userID, agentID, key string, dingTalk bool) scenesDetailResponse {
	t.Helper()
	w := scenesAdminAs(t, router, userID, http.MethodGet, scenesPath(agentID, key), nil, dingTalk)
	ctxcapExpectStatus(t, w, http.StatusOK, "admin scene detail")
	var detail scenesDetailResponse
	ctxcapDecode(t, w, &detail)
	return detail
}

// sceneConfigNull reports whether a decoded JSON value is null or absent.
func sceneConfigNull(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed == "" || trimmed == "null"
}

func sceneConfigOffered(t *testing.T, detail scenesDetailResponse, connectorID string) agentSceneOfferedConnectorDTO {
	t.Helper()
	for _, c := range detail.Offers.Connectors {
		if c.ID == connectorID {
			return c
		}
	}
	t.Fatalf("connector %s not offered in %+v", connectorID, detail.Offers.Connectors)
	return agentSceneOfferedConnectorDTO{}
}

// The admin scene page configures where the scene's configuration lives: a
// group's own scope, or a 1:1 chat's person. It shows that scope, its
// custom MCP servers and the credential of each offered connector, and says
// whether the caller may connect accounts there.
func TestAgentSceneConfigLivesInTheScopeOfTheScene(t *testing.T) {
	f := newCtxcapFixture(t)
	f.cleanupScenes(t)
	f.cleanupGrantsAndLinks(t)
	router := scenesRouter(f.h)
	agentID := uuidToString(f.agent)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM context_scope_mcp_config WHERE agent_id = $1`, agentID)
	})
	mcpPath := func(key string) string { return scenesPath(agentID, key) + "/mcp-config" }
	servers := map[string]any{"mcpServers": map[string]any{"docs": map[string]any{"type": "http", "url": "https://docs.example.test/mcp"}}}

	// A group scene: its own scope. can_connect needs a DingTalk sign-in,
	// because accounts are connected through the configure page's routes.
	detail := sceneConfigDetail(t, router, "", agentID, ctxcapScene, false)
	if detail.Scope == nil || *detail.Scope != (contextCapScopeRef{Type: contextcap.ScopeScene, Key: ctxcapScene, Title: detail.Scene.Title}) ||
		detail.CanConnect || !sceneConfigNull(detail.MCPConfig) || detail.MCPConfigRedacted {
		t.Fatalf("group detail without a DingTalk sign-in = %+v", detail)
	}
	detail = sceneConfigDetail(t, router, "", agentID, ctxcapScene, true)
	if !detail.CanConnect {
		t.Fatalf("group detail from a DingTalk sign-in = %+v", detail)
	}
	bearer := sceneConfigOffered(t, detail, f.scene)
	if !bearer.AcceptsCredential || bearer.AcceptsPAT || bearer.OAuthAvailable || bearer.InstallURL != "" || bearer.Credential.Connected {
		t.Fatalf("offered bearer connector = %+v", bearer)
	}
	if none := sceneConfigOffered(t, detail, f.person); none.AcceptsCredential || none.Credential.Connected {
		t.Fatalf("offered no-auth connector = %+v", none)
	}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, "/api/context-capabilities/agents/"+agentID+"/credentials", testUserID, map[string]string{
		"scope_type": contextcap.ScopeScene, "scope_key": ctxcapScene, "connector_id": f.scene, "bearer": "scene-config-token",
	}), http.StatusOK, "manager stores a group credential")
	detail = sceneConfigDetail(t, router, "", agentID, ctxcapScene, false)
	if got := sceneConfigOffered(t, detail, f.scene).Credential; got != (agentSceneCredentialDTO{Connected: true, Account: "••••oken"}) {
		t.Fatalf("group credential = %+v", got)
	}
	if strings.Contains(scenesAs(t, router, "", http.MethodGet, scenesPath(agentID, ctxcapScene), nil).Body.String(), "scene-config-token") {
		t.Fatal("scene detail leaks the credential")
	}

	// Custom MCP servers of the group: stored in the scene scope.
	w := scenesAs(t, router, "", http.MethodPut, mcpPath(ctxcapScene), map[string]any{"mcp_config": servers})
	ctxcapExpectStatus(t, w, http.StatusOK, "put group MCP config")
	var put struct {
		MCPConfig json.RawMessage `json:"mcp_config"`
	}
	ctxcapDecode(t, w, &put)
	if !strings.Contains(string(put.MCPConfig), "https://docs.example.test/mcp") {
		t.Fatalf("put response = %s", w.Body.String())
	}
	if stored, err := contextcap.GetScopeMCPConfig(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopeScene, ctxcapOrg, ctxcapScene); err != nil ||
		!strings.Contains(string(stored.MCPConfig), "docs.example.test") || stored.UpdatedBy != testUserID {
		t.Fatalf("stored group MCP config = %+v %v", stored, err)
	}
	if detail = sceneConfigDetail(t, router, "", agentID, ctxcapScene, false); !strings.Contains(string(detail.MCPConfig), "docs.example.test") {
		t.Fatalf("group detail MCP config = %s", detail.MCPConfig)
	}
	for name, body := range map[string]any{
		"missing field": map[string]any{},
		"array":         map[string]any{"mcp_config": []any{1}},
		"string":        map[string]any{"mcp_config": "x"},
		"unknown field": map[string]any{"mcp_config": servers, "scope": "person"},
		"too large":     map[string]any{"mcp_config": map[string]any{"x": strings.Repeat("a", contextcap.MaxScopeMCPConfigBytes)}},
	} {
		ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, mcpPath(ctxcapScene), body), http.StatusBadRequest, name)
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, mcpPath("cidSceneConfigNeverSeen=="), map[string]any{"mcp_config": servers}), http.StatusNotFound, "unknown scene")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, "/api/agents/"+agentID+"/scenes/not-a-cid/mcp-config", map[string]any{"mcp_config": servers}), http.StatusBadRequest, "malformed scene key")
	member := createPermissionTestMember(t, "scene-config-member-"+uuid.NewString()[:8]+"@example.test")
	owner := createPermissionTestMember(t, "scene-config-owner-"+uuid.NewString()[:8]+"@example.test")
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE user_id = ANY($1::uuid[])`, []string{member, owner})
	})
	ctxcapExpectStatus(t, scenesAs(t, router, member, http.MethodPut, mcpPath(ctxcapScene), map[string]any{"mcp_config": servers}), http.StatusForbidden, "plain member")
	w = scenesAs(t, router, "", http.MethodPut, mcpPath(ctxcapScene), map[string]any{"mcp_config": nil})
	ctxcapExpectStatus(t, w, http.StatusOK, "clear group MCP config")
	if strings.TrimSpace(w.Body.String()) != `{"mcp_config":null}` {
		t.Fatalf("clear response = %s", w.Body.String())
	}
	if _, err := contextcap.GetScopeMCPConfig(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopeScene, ctxcapOrg, ctxcapScene); err == nil {
		t.Fatal("a cleared MCP config kept its row")
	}

	// A 1:1 chat whose person is unknown: no configuration, writes refused.
	if err := contextcap.RegisterDirectScene(ctx, testPool, testWorkspaceID, agentID, ctxcapOrg, sceneConfigDirect, "Dora"); err != nil {
		t.Fatal(err)
	}
	detail = sceneConfigDetail(t, router, "", agentID, sceneConfigDirect, true)
	if detail.Scope != nil || detail.CanConnect || len(detail.Bindings) != 0 || !sceneConfigNull(detail.MCPConfig) || detail.Scene.Kind != "dm" {
		t.Fatalf("DM of an unknown person = %+v", detail)
	}
	for name, w := range map[string]*httptest.ResponseRecorder{
		"mcp config": scenesAs(t, router, "", http.MethodPut, mcpPath(sceneConfigDirect), map[string]any{"mcp_config": servers}),
		"binding": scenesAs(t, router, "", http.MethodPut, scenesPath(agentID, sceneConfigDirect)+"/bindings",
			map[string]any{"resource_type": "connector", "resource_id": f.person, "enabled": true}),
	} {
		if w.Code != http.StatusConflict || catalogErrorCode(t, w) != contextCapErrDMPersonUnknown {
			t.Fatalf("%s write to a DM of an unknown person: %d %s", name, w.Code, w.Body.String())
		}
	}
	// The prompt belongs to the scene itself.
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, scenesPath(agentID, sceneConfigDirect)+"/prompt", map[string]any{"prompt": "Be brief."}), http.StatusOK, "DM prompt")

	// Dora writes in the chat: her personal scope is the DM's configuration.
	f.coordinatorDMJob(t, sceneConfigDirect, "Dora", sceneConfigDirectStaff, time.Minute)
	detail = sceneConfigDetail(t, router, "", agentID, sceneConfigDirect, true)
	if detail.Scope == nil || *detail.Scope != (contextCapScopeRef{Type: contextcap.ScopePerson, Key: sceneConfigDirectStaff, Title: "Dora"}) ||
		detail.CanConnect || detail.Prompt.Text != "Be brief." {
		t.Fatalf("DM of Dora for a manager = %+v", detail)
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, mcpPath(sceneConfigDirect), map[string]any{"mcp_config": servers}), http.StatusOK, "DM MCP config")
	if _, err := contextcap.GetScopeMCPConfig(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopePerson, ctxcapOrg, sceneConfigDirectStaff); err != nil {
		t.Fatalf("DM MCP config not in the person scope: %v", err)
	}
	if _, err := contextcap.GetScopeMCPConfig(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopeScene, ctxcapOrg, sceneConfigDirect); err == nil {
		t.Fatal("DM MCP config stored on the scene key")
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, scenesPath(agentID, sceneConfigDirect)+"/bindings",
		map[string]any{"resource_type": "connector", "resource_id": f.person, "enabled": true}), http.StatusOK, "DM binding")
	if stored, err := contextcap.ListScopeBindings(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopePerson, ctxcapOrg, sceneConfigDirectStaff); err != nil ||
		!ctxcapStoredBinding(stored, f.person, true, testUserID) {
		t.Fatalf("DM binding not in the person scope: %+v %v", stored, err)
	}

	// Dora's own account: its hint reaches workspace admins and Dora, not
	// an agent owner who is only a member.
	key := contextcap.CredentialBinding{WorkspaceID: testWorkspaceID, AgentID: agentID, ConnectorID: f.scene,
		ScopeType: contextcap.ScopePerson, OrgID: ctxcapOrg, ScopeKey: sceneConfigDirectStaff}
	sealed, err := contextcap.SealCredential(f.box, key, "dora-personal-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contextcap.UpsertCredential(ctx, testPool, key, sealed, contextcap.Hint("dora-personal-token"), ""); err != nil {
		t.Fatal(err)
	}
	detail = sceneConfigDetail(t, router, "", agentID, sceneConfigDirect, true)
	if got := sceneConfigOffered(t, detail, f.scene).Credential; got != (agentSceneCredentialDTO{Connected: true, Account: "••••oken"}) || !strings.Contains(string(detail.MCPConfig), "docs") {
		t.Fatalf("DM detail for a workspace admin: credential=%+v mcp=%s", got, detail.MCPConfig)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent SET owner_id = $1 WHERE id = $2`, owner, agentID); err != nil {
		t.Fatal(err)
	}
	detail = sceneConfigDetail(t, router, owner, agentID, sceneConfigDirect, true)
	if got := sceneConfigOffered(t, detail, f.scene).Credential; got != (agentSceneCredentialDTO{Connected: true}) || detail.CanConnect {
		t.Fatalf("DM detail for the agent owner: credential=%+v can_connect=%v", got, detail.CanConnect)
	}
	// Dora is the agent owner: her own account and connect.
	f.grant(t, owner, contextcap.ScopePerson, sceneConfigDirectStaff, "Dora")
	detail = sceneConfigDetail(t, router, owner, agentID, sceneConfigDirect, true)
	if got := sceneConfigOffered(t, detail, f.scene).Credential; got.Account != "••••oken" || !detail.CanConnect {
		t.Fatalf("DM detail for Dora: credential=%+v can_connect=%v", got, detail.CanConnect)
	}

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
	detail = sceneConfigDetail(t, router, "", agentID, sceneConfigDirect, false)
	if !sceneConfigNull(detail.MCPConfig) || !detail.MCPConfigRedacted {
		t.Fatalf("redacted DM detail: mcp=%s redacted=%v", detail.MCPConfig, detail.MCPConfigRedacted)
	}
}
