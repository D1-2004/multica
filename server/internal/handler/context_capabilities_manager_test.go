package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/contextcap"
)

const (
	ctxcapManagerDirect      = "cidCtxcapManagerDirect=="
	ctxcapManagerDirectStaff = "staff-ctxcap-bob"
)

// ctxcapStoredBinding reports whether bindings hold resourceID with enabled,
// last written by updatedBy.
func ctxcapStoredBinding(bindings []contextcap.Binding, resourceID string, enabled bool, updatedBy string) bool {
	for _, binding := range bindings {
		if binding.ResourceID == resourceID {
			return binding.Enabled == enabled && binding.UpdatedBy == updatedBy
		}
	}
	return false
}

type ctxcapAgentsList struct {
	Agents []struct {
		ID     string               `json:"id"`
		Access string               `json:"access"`
		Scopes []contextCapGrantDTO `json:"scopes"`
	} `json:"agents"`
}

// ctxcapListedAccess returns the access and scope count of agentID in the
// caller's configure-page agent list, or ok=false when it is not listed.
func ctxcapListedAccess(t *testing.T, router http.Handler, userID, agentID string) (string, int, bool) {
	t.Helper()
	w := ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents", userID, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "list agents")
	var list ctxcapAgentsList
	ctxcapDecode(t, w, &list)
	for _, agent := range list.Agents {
		if agent.ID == agentID {
			return agent.Access, len(agent.Scopes), true
		}
	}
	return "", 0, false
}

// A manager of the agent (workspace owner/admin, or the agent owner) opens
// the configure page without a link and configures every scene of the agent;
// the person scope still needs the person grant, and every offer gate stays.
func TestContextCapabilitiesManagerAccess(t *testing.T) {
	f := newCtxcapFixture(t)
	f.cleanupScenes(t)
	f.cleanupGrantsAndLinks(t)
	router := ctxcapRouter(f.h)
	agentID := uuidToString(f.agent)
	ctx := context.Background()
	if err := contextcap.RegisterDirectScene(ctx, testPool, testWorkspaceID, agentID, ctxcapOrg, ctxcapManagerDirect, "Direct with Bob"); err != nil {
		t.Fatal(err)
	}
	owner := createPermissionTestMember(t, "ctxcap-owner-"+uuid.NewString()[:8]+"@example.test")
	member := createPermissionTestMember(t, "ctxcap-member-"+uuid.NewString()[:8]+"@example.test")
	if _, err := testPool.Exec(ctx, `UPDATE agent SET owner_id = $1 WHERE id = $2`, owner, agentID); err != nil {
		t.Fatal(err)
	}
	holder := uuid.NewString()
	f.grant(t, holder, contextcap.ScopeScene, ctxcapScene, "Ctxcap group")

	// Agent list: managers see the agent without a grant; a plain member
	// does not; a grant holder sees it through the grant.
	for _, tc := range []struct {
		name, user, access string
		scopes             int
		listed             bool
	}{
		{"workspace owner", testUserID, contextCapAccessManager, 0, true},
		{"agent owner", owner, contextCapAccessManager, 0, true},
		{"plain member", member, "", 0, false},
		{"grant holder", holder, contextCapAccessGrant, 1, true},
	} {
		access, scopes, listed := ctxcapListedAccess(t, router, tc.user, agentID)
		if listed != tc.listed || access != tc.access || scopes != tc.scopes {
			t.Fatalf("%s: listed=%v access=%q scopes=%d", tc.name, listed, access, scopes)
		}
	}

	// Agent detail: a manager gets every scene of the agent (group and 1:1).
	w := ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID, owner, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "manager detail")
	var detail struct {
		ctxcapAgentDetail
		Access string `json:"access"`
	}
	ctxcapDecode(t, w, &detail)
	scenes := map[string]contextCapSceneDTO{}
	for _, scene := range detail.Scenes {
		scenes[scene.ScopeKey] = scene
	}
	if detail.Access != contextCapAccessManager || detail.Person != nil || detail.JSAPIAvailable || len(detail.Offers.Connectors) != 2 {
		t.Fatalf("manager detail = %+v", detail)
	}
	if got := scenes[ctxcapScene]; got.Source != contextCapSourceManager || got.Kind != "group" || got.ExpiresAt != "" {
		t.Fatalf("manager group scene = %+v (all %+v)", got, detail.Scenes)
	}
	if got := scenes[ctxcapManagerDirect]; got.Source != contextCapSourceManager || got.Kind != "dm" || got.ScopeTitle != "Direct with Bob" {
		t.Fatalf("manager DM scene = %+v", got)
	}
	w = ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID, holder, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "grant holder detail")
	detail.Scenes = nil
	ctxcapDecode(t, w, &detail)
	if detail.Access != contextCapAccessGrant || len(detail.Scenes) != 1 || detail.Scenes[0].ScopeKey != ctxcapScene || detail.Scenes[0].Source != contextcap.GrantSourceAgentLink {
		t.Fatalf("grant holder detail = %+v", detail)
	}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID, member, nil), http.StatusForbidden, "plain member detail")

	// Scene detail.
	w = ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, ctxcapManagerDirect), owner, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "manager DM scene")
	var scene ctxcapSceneDetail
	ctxcapDecode(t, w, &scene)
	if scene.Scene.Kind != "dm" || scene.Scene.Source != contextCapSourceManager || scene.Scene.ScopeTitle != "Direct with Bob" {
		t.Fatalf("manager DM scene = %+v", scene)
	}
	w = ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, ctxcapScene), testUserID, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "manager group scene")
	ctxcapDecode(t, w, &scene)
	if scene.Scene.Kind != "group" || !ctxcapHasBinding(scene.Bindings, f.scene, true) || ctxcapMentions(scene.Bindings, f.notOffered) {
		t.Fatalf("manager group scene = %+v", scene)
	}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, "cidCtxcapNeverSeen=="), owner, nil), http.StatusNotFound, "manager, unknown scene")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, ctxcapScene), member, nil), http.StatusForbidden, "plain member scene")

	// Bindings: a manager toggles offered items of a scene; offer gates and
	// the person scope stay closed.
	bindingsPath := "/api/context-capabilities/agents/" + agentID + "/bindings"
	body := func(scopeType, key, resourceID string, enabled bool) map[string]any {
		return map[string]any{"scope_type": scopeType, "scope_key": key, "resource_type": contextcap.ResourceConnector, "resource_id": resourceID, "enabled": enabled}
	}
	w = ctxcapMobile(t, router, http.MethodPut, bindingsPath, owner, body(contextcap.ScopeScene, ctxcapScene, f.person, true))
	ctxcapExpectStatus(t, w, http.StatusOK, "manager enables an offered connector")
	stored, err := contextcap.ListScopeBindings(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopeScene, ctxcapOrg, ctxcapScene)
	if err != nil || !ctxcapStoredBinding(stored, f.person, true, owner) {
		t.Fatalf("stored manager binding = %+v %v", stored, err)
	}
	// A 1:1 chat's configuration is its person's. While nobody knows who
	// that is, it reads without configuration and refuses writes.
	w = ctxcapMobile(t, router, http.MethodPut, bindingsPath, owner, body(contextcap.ScopeScene, ctxcapManagerDirect, f.person, true))
	if w.Code != http.StatusConflict || catalogErrorCode(t, w) != contextCapErrDMPersonUnknown {
		t.Fatalf("manager, DM of an unknown person: %d %s", w.Code, w.Body.String())
	}
	w = ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, ctxcapManagerDirect), owner, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "manager DM scene of an unknown person")
	scene = ctxcapSceneDetail{}
	ctxcapDecode(t, w, &scene)
	if scene.Scope != nil || scene.CanConnect || len(scene.Bindings) != 0 || scene.Scene.Kind != "dm" {
		t.Fatalf("manager DM scene of an unknown person = %+v", scene)
	}
	// The Coordinator saw Bob write in that chat: the DM is Bob's, and the
	// manager's toggle lands in Bob's personal scope.
	f.coordinatorDMJob(t, ctxcapManagerDirect, "Bob", ctxcapManagerDirectStaff, time.Minute)
	w = ctxcapMobile(t, router, http.MethodPut, bindingsPath, owner, body(contextcap.ScopeScene, ctxcapManagerDirect, f.person, true))
	ctxcapExpectStatus(t, w, http.StatusOK, "manager enables a connector in a DM")
	stored, err = contextcap.ListScopeBindings(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopePerson, ctxcapOrg, ctxcapManagerDirectStaff)
	if err != nil || len(stored) != 1 || !ctxcapStoredBinding(stored, f.person, true, owner) || stored[0].ScopeTitle != "Bob" {
		t.Fatalf("stored DM binding = %+v %v", stored, err)
	}
	if stored, err := contextcap.ListScopeBindings(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopeScene, ctxcapOrg, ctxcapManagerDirect); err != nil || len(stored) != 0 {
		t.Fatalf("DM scene scope rows = %+v %v", stored, err)
	}
	w = ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, ctxcapManagerDirect), owner, nil)
	scene = ctxcapSceneDetail{}
	ctxcapDecode(t, w, &scene)
	if scene.Scope == nil || *scene.Scope != (contextCapScopeRef{Type: contextcap.ScopePerson, Key: ctxcapManagerDirectStaff, Title: "Bob"}) ||
		scene.CanConnect || !ctxcapHasBinding(scene.Bindings, f.person, true) || scene.Scene.Source != contextCapSourceManager {
		t.Fatalf("manager DM scene of Bob = %+v", scene)
	}
	// Bob's own account: the agent owner (a plain member) sees that it is
	// connected but not the account hint; a workspace admin sees both.
	f.setCredential(t, f.person, contextcap.ScopePerson, ctxcapManagerDirectStaff, "bob-personal-token-abcd")
	for _, tc := range []struct {
		name, user, hint string
	}{
		{"agent owner", owner, ""},
		{"workspace owner", testUserID, "••••abcd"},
	} {
		w = ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, ctxcapManagerDirect), tc.user, nil)
		ctxcapExpectStatus(t, w, http.StatusOK, tc.name+" reads Bob's DM")
		scene = ctxcapSceneDetail{}
		ctxcapDecode(t, w, &scene)
		if len(scene.Credentials) != 1 || scene.Credentials[0].ConnectorID != f.person || scene.Credentials[0].Kind != "bearer" ||
			scene.Credentials[0].UpdatedAt == "" || scene.Credentials[0].Hint != tc.hint || scene.CanConnect {
			t.Fatalf("%s: Bob's DM credentials = %+v can_connect=%v", tc.name, scene.Credentials, scene.CanConnect)
		}
	}
	// share_in_groups is Bob's own opt-in: Bob may set it through his 1:1
	// chat (it lands in his personal scope), a manager may not.
	shareBody := body(contextcap.ScopeScene, ctxcapManagerDirect, f.person, true)
	shareBody["share_in_groups"] = true
	w = ctxcapMobile(t, router, http.MethodPut, bindingsPath, owner, shareBody)
	if w.Code != http.StatusForbidden || catalogErrorCode(t, w) != contextCapErrPersonOnly {
		t.Fatalf("manager, share_in_groups in a DM: %d %s", w.Code, w.Body.String())
	}
	bob := uuid.NewString()
	f.grant(t, bob, contextcap.ScopePerson, ctxcapManagerDirectStaff, "Bob")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, bindingsPath, bob, shareBody), http.StatusOK, "Bob opts in through his DM")
	stored, err = contextcap.ListScopeBindings(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopePerson, ctxcapOrg, ctxcapManagerDirectStaff)
	if err != nil || len(stored) != 1 || !stored[0].ShareInGroups {
		t.Fatalf("Bob's opt-in through his DM = %+v %v", stored, err)
	}
	groupShare := body(contextcap.ScopeScene, ctxcapScene, f.person, true)
	groupShare["share_in_groups"] = true
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, bindingsPath, owner, groupShare), http.StatusBadRequest, "share_in_groups on a group scene")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, bindingsPath, owner, body(contextcap.ScopeScene, ctxcapScene, f.notOffered, true)), http.StatusForbidden, "manager, unoffered connector")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, bindingsPath, owner, body(contextcap.ScopeScene, ctxcapScene, f.global, true)), http.StatusForbidden, "manager, granted but unoffered connector")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, bindingsPath, owner, body(contextcap.ScopePerson, ctxcapStaff, f.person, true)), http.StatusForbidden, "manager, person scope")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, bindingsPath, owner, body(contextcap.ScopeScene, "cidCtxcapNeverSeen==", f.person, true)), http.StatusNotFound, "manager, unknown scene binding")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, bindingsPath, member, body(contextcap.ScopeScene, ctxcapScene, f.person, true)), http.StatusForbidden, "plain member binding")

	// Credentials: a scene credential needs the offer, even for a manager.
	credentialsPath := "/api/context-capabilities/agents/" + agentID + "/credentials"
	credential := func(scopeType, key, connectorID string) map[string]string {
		return map[string]string{"scope_type": scopeType, "scope_key": key, "connector_id": connectorID, "bearer": "manager-scene-token"}
	}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, credentialsPath, testUserID, credential(contextcap.ScopeScene, ctxcapScene, f.scene)), http.StatusOK, "manager scene credential")
	w = ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, ctxcapScene), testUserID, nil)
	ctxcapDecode(t, w, &scene)
	if len(scene.Credentials) != 1 || scene.Credentials[0].ConnectorID != f.scene || scene.Credentials[0].Hint != "••••oken" {
		t.Fatalf("manager scene credentials = %+v", scene.Credentials)
	}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, credentialsPath, testUserID, credential(contextcap.ScopeScene, ctxcapScene, f.global)), http.StatusForbidden, "manager, scene credential for an unoffered connector")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, credentialsPath, testUserID, credential(contextcap.ScopePerson, ctxcapStaff, f.global)), http.StatusForbidden, "manager, person credential")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, credentialsPath, member, credential(contextcap.ScopeScene, ctxcapScene, f.scene)), http.StatusForbidden, "plain member credential")
	// A 1:1 chat's credentials are its person's: a manager may not connect
	// or remove someone else's account.
	w = ctxcapMobile(t, router, http.MethodPut, credentialsPath, owner, credential(contextcap.ScopeScene, ctxcapManagerDirect, f.scene))
	if w.Code != http.StatusForbidden || catalogErrorCode(t, w) != contextCapErrPersonOnly {
		t.Fatalf("manager, DM credential: %d %s", w.Code, w.Body.String())
	}
	w = ctxcapMobile(t, router, http.MethodDelete, credentialsPath+"?scope_type=scene&scope_key="+ctxcapManagerDirect+"&connector_id="+f.scene, owner, nil)
	if w.Code != http.StatusForbidden || catalogErrorCode(t, w) != contextCapErrPersonOnly {
		t.Fatalf("manager, DM credential delete: %d %s", w.Code, w.Body.String())
	}
	deletePath := credentialsPath + "?scope_type=scene&scope_key=" + ctxcapScene + "&connector_id=" + f.scene
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodDelete, deletePath, member, nil), http.StatusForbidden, "plain member delete")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodDelete, deletePath, owner, nil), http.StatusNoContent, "manager delete")
	if _, err := contextcap.GetCredential(ctx, testPool, contextcap.CredentialBinding{WorkspaceID: testWorkspaceID, AgentID: agentID, ConnectorID: f.scene,
		ScopeType: contextcap.ScopeScene, OrgID: ctxcapOrg, ScopeKey: ctxcapScene}); err == nil {
		t.Fatal("scene credential survived the manager's delete")
	}

	// A manager is not the person: a person grant is still required, and
	// the list reports manager access next to the grant.
	f.grant(t, owner, contextcap.ScopePerson, ctxcapStaff, "Owner")
	if access, scopes, _ := ctxcapListedAccess(t, router, owner, agentID); access != contextCapAccessManager || scopes != 1 {
		t.Fatalf("manager with a person grant: access=%q scopes=%d", access, scopes)
	}
	w = ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID, owner, nil)
	detail.Person = nil
	ctxcapDecode(t, w, &detail)
	if detail.Person == nil || detail.Person.ScopeKey != ctxcapStaff || detail.Access != contextCapAccessManager {
		t.Fatalf("manager with a person grant detail = %+v", detail)
	}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, bindingsPath, owner, body(contextcap.ScopePerson, ctxcapStaff, f.person, true)), http.StatusOK, "person grant binding")

	// Archived agents are not configurable, even for managers.
	if _, err := testPool.Exec(ctx, `UPDATE agent SET archived_at = now() WHERE id = $1`, agentID); err != nil {
		t.Fatal(err)
	}
	if _, _, listed := ctxcapListedAccess(t, router, testUserID, agentID); listed {
		t.Fatal("archived agent listed for its manager")
	}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID, testUserID, nil), http.StatusNotFound, "archived agent detail")
}
