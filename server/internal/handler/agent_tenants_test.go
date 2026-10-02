package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/contextcap"
)

type tenantsListResponse struct {
	Tenants        []agentTenantDTO        `json:"tenants"`
	UnassignedOrgs []agentUnassignedOrgDTO `json:"unassigned_orgs"`
}

func tenantByOrg(t *testing.T, list tenantsListResponse, orgID string) agentTenantDTO {
	t.Helper()
	for _, tenant := range list.Tenants {
		if tenant.OrgID == orgID {
			return tenant
		}
	}
	t.Fatalf("tenant %s not in %+v", orgID, list.Tenants)
	return agentTenantDTO{}
}

func TestAgentTenantsLifecycle(t *testing.T) {
	f := newCtxcapFixture(t)
	f.cleanupScenes(t)
	f.cleanupGrantsAndLinks(t)
	router := scenesRouter(f.h)
	agentID := uuidToString(f.agent)
	base := "/api/agents/" + agentID + "/tenants"
	list := func() tenantsListResponse {
		t.Helper()
		w := scenesAs(t, router, "", http.MethodGet, base, nil)
		ctxcapExpectStatus(t, w, http.StatusOK, "list tenants")
		var out tenantsListResponse
		ctxcapDecode(t, w, &out)
		return out
	}

	// The identity org is a tenant from the start; an org seen only in data
	// is unassigned.
	f.coordinatorJob(t, "cidTenantsForeign==", "group", "Foreign group", "Bob", "org-foreign", time.Minute)
	f.coordinatorDMJob(t, "cidTenantsDirect==", "Ann", "staff-tenants-ann", time.Minute)
	annDM := f.sceneFor(t, ctxcapOrg, "dm", "cidTenantsDirect==", "", time.Minute)
	got := list()
	if len(got.Tenants) != 1 || got.Tenants[0].OrgID != ctxcapOrg || got.Tenants[0].Source != contextcap.TenantSourceIdentity {
		t.Fatalf("tenants = %+v", got.Tenants)
	}
	// The fixture's two group scenes; staffs are the person binding and the
	// 1:1 sender.
	if identity := got.Tenants[0]; identity.GroupCount != 2 || identity.PersonCount != 2 {
		t.Fatalf("identity counts = %+v", identity)
	}
	if len(got.UnassignedOrgs) != 1 || got.UnassignedOrgs[0] != (agentUnassignedOrgDTO{OrgID: "org-foreign", GroupCount: 1}) {
		t.Fatalf("unassigned = %+v", got.UnassignedOrgs)
	}

	// Create: validated, conflicts, then listed with its counts.
	for name, body := range map[string]map[string]string{
		"bad org":   {"org_id": "org foreign", "name": "Foreign"},
		"long org":  {"org_id": strings.Repeat("o", 65), "name": "Foreign"},
		"no name":   {"org_id": "org-foreign", "name": "  "},
		"long name": {"org_id": "org-foreign", "name": strings.Repeat("名", 65)},
	} {
		ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPost, base, body), http.StatusBadRequest, name)
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPost, base, map[string]string{"org_id": "org-foreign", "name": "x", "extra": "1"}), http.StatusBadRequest, "unknown field")
	w := scenesAs(t, router, "", http.MethodPost, base, map[string]string{"org_id": "org-foreign", "name": " Foreign Corp "})
	ctxcapExpectStatus(t, w, http.StatusCreated, "create tenant")
	var created struct {
		Tenant agentTenantDTO `json:"tenant"`
	}
	ctxcapDecode(t, w, &created)
	if created.Tenant != (agentTenantDTO{OrgID: "org-foreign", Name: "Foreign Corp", Source: contextcap.TenantSourceCreated, GroupCount: 1}) {
		t.Fatalf("created = %+v", created.Tenant)
	}
	for _, org := range []string{"org-foreign", ctxcapOrg} {
		w = scenesAs(t, router, "", http.MethodPost, base, map[string]string{"org_id": org, "name": "Again"})
		if w.Code != http.StatusConflict || catalogErrorCode(t, w) != agentTenantErrExists {
			t.Fatalf("duplicate %s: %d %s", org, w.Code, w.Body.String())
		}
	}
	got = list()
	if len(got.Tenants) != 2 || len(got.UnassignedOrgs) != 0 || tenantByOrg(t, got, "org-foreign").Name != "Foreign Corp" {
		t.Fatalf("after create = %+v", got)
	}

	// Groups and people per tenant.
	w = scenesAs(t, router, "", http.MethodGet, base+"/org-foreign/groups", nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "foreign groups")
	var groups scenesListResponse
	ctxcapDecode(t, w, &groups)
	if len(groups.Scenes) != 1 || groups.Scenes[0].ConversationID != "cidTenantsForeign==" || groups.Scenes[0].Kind != "group" || groups.HasMore {
		t.Fatalf("foreign groups = %+v", groups)
	}
	w = scenesAs(t, router, "", http.MethodGet, base+"/"+ctxcapOrg+"/groups?groups_only=true", nil)
	ctxcapDecode(t, w, &groups)
	if got := scenesByKey(groups.Scenes); len(got) != 2 || got[ctxcapScene].Kind != "group" || got[ctxcapOtherScene].Kind != "group" {
		t.Fatalf("identity groups (no 1:1 chats) = %+v", groups)
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodGet, base+"/"+ctxcapOrg+"/groups?limit=0", nil), http.StatusBadRequest, "bad limit")
	w = scenesAs(t, router, "", http.MethodGet, base+"/"+ctxcapOrg+"/persons", nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "identity persons")
	var persons struct {
		Persons []agentTenantPersonDTO `json:"persons"`
	}
	ctxcapDecode(t, w, &persons)
	byStaff := map[string]agentTenantPersonDTO{}
	for _, p := range persons.Persons {
		byStaff[p.StaffID] = p
	}
	if len(byStaff) != 2 || byStaff["staff-tenants-ann"].DMSceneKey != annDM || byStaff["staff-tenants-ann"].Title != "Ann" ||
		byStaff[ctxcapStaff].LastActiveAt == "" {
		t.Fatalf("identity persons = %+v", persons.Persons)
	}
	w = scenesAs(t, router, "", http.MethodGet, base+"/org-foreign/persons", nil)
	ctxcapDecode(t, w, &persons)
	if len(persons.Persons) != 0 {
		t.Fatalf("foreign persons = %+v", persons.Persons)
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodGet, base+"/org-nobody/groups", nil), http.StatusNotFound, "unknown tenant")

	// Rename: a created tenant, and the identity tenant (stores a row).
	w = scenesAs(t, router, "", http.MethodPatch, base+"/org-foreign", map[string]string{"name": "Foreign Ltd"})
	ctxcapExpectStatus(t, w, http.StatusOK, "rename")
	ctxcapDecode(t, w, &created)
	if created.Tenant.Name != "Foreign Ltd" || created.Tenant.GroupCount != 1 {
		t.Fatalf("renamed = %+v", created.Tenant)
	}
	w = scenesAs(t, router, "", http.MethodPatch, base+"/"+ctxcapOrg, map[string]string{"name": "总部"})
	ctxcapExpectStatus(t, w, http.StatusOK, "rename identity")
	ctxcapDecode(t, w, &created)
	if created.Tenant.Name != "总部" || created.Tenant.Source != contextcap.TenantSourceIdentity {
		t.Fatalf("renamed identity = %+v", created.Tenant)
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPatch, base+"/org-nobody", map[string]string{"name": "x"}), http.StatusNotFound, "rename unknown")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPatch, base+"/org-foreign", map[string]string{"name": ""}), http.StatusBadRequest, "rename to empty")

	// Only managers; agents never.
	member := createPermissionTestMember(t, "tenants-member-"+uuid.NewString()[:8]+"@example.test")
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE user_id = $1`, member) })
	ctxcapExpectStatus(t, scenesAs(t, router, member, http.MethodGet, base, nil), http.StatusForbidden, "plain member list")
	ctxcapExpectStatus(t, scenesAs(t, router, member, http.MethodPost, base, map[string]string{"org_id": "org-member", "name": "x"}), http.StatusForbidden, "plain member create")
	ctxcapExpectStatus(t, scenesAs(t, router, member, http.MethodPatch, base+"/org-foreign", map[string]string{"name": "x"}), http.StatusForbidden, "plain member rename")
	ctxcapExpectStatus(t, scenesAs(t, router, member, http.MethodDelete, base+"/org-foreign", nil), http.StatusForbidden, "plain member delete")
	for _, call := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, base, nil},
		{http.MethodPost, base, map[string]string{"org_id": "org-agent", "name": "x"}},
		{http.MethodPatch, base + "/org-foreign", map[string]string{"name": "x"}},
		{http.MethodDelete, base + "/org-foreign", nil},
	} {
		taskActor := newRequest(call.method, call.path, call.body)
		taskActor.Header.Set("X-Actor-Source", "task_token")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, taskActor)
		ctxcapExpectStatus(t, rec, http.StatusForbidden, "task token "+call.method)
	}
	if tenant := tenantByOrg(t, list(), "org-foreign"); tenant.Name != "Foreign Ltd" {
		t.Fatalf("tenant after refused calls = %+v", tenant)
	}

	// Delete: the identity tenant cannot go; a created one takes its
	// org-level configuration with it and its org shows as unassigned.
	w = scenesAs(t, router, "", http.MethodDelete, base+"/"+ctxcapOrg, nil)
	if w.Code != http.StatusConflict || catalogErrorCode(t, w) != agentTenantErrIdentity {
		t.Fatalf("delete identity: %d %s", w.Code, w.Body.String())
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, ctxNodePath(agentID, "org-foreign", contextcap.ScopeOrg, "org-foreign")+"/bindings",
		map[string]any{"resource_type": "skill", "resource_id": f.skillScene, "enabled": true}), http.StatusOK, "foreign org binding")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodDelete, base+"/org-foreign", nil), http.StatusNoContent, "delete tenant")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodDelete, base+"/org-foreign", nil), http.StatusNotFound, "delete twice")
	if stored, err := contextcap.ListScopeBindings(context.Background(), testPool, testWorkspaceID, agentID, contextcap.ScopeOrg, "org-foreign", "org-foreign"); err != nil || len(stored) != 0 {
		t.Fatalf("org bindings after delete = %+v %v", stored, err)
	}
	got = list()
	if len(got.Tenants) != 1 || got.Tenants[0].Name != "总部" || len(got.UnassignedOrgs) != 1 || got.UnassignedOrgs[0].OrgID != "org-foreign" {
		t.Fatalf("after delete = %+v", got)
	}
}

// The configure page works in one tenant org at a time (org_id), shows the
// enterprise layer read-only to members of that org and lets managers
// configure it.
func TestContextConfigMobileTenantOrg(t *testing.T) {
	f := newCtxcapFixture(t)
	f.cleanupScenes(t)
	f.cleanupGrantsAndLinks(t)
	router := ctxcapRouter(f.h)
	agentID := uuidToString(f.agent)
	ctx := context.Background()
	const betaOrg = "org-ctxcap-beta"
	if _, err := contextcap.CreateTenant(ctx, testPool, contextcap.TenantWrite{WorkspaceID: testWorkspaceID, AgentID: agentID, OrgID: betaOrg, Name: "Beta"}); err != nil {
		t.Fatal(err)
	}
	betaScene := f.sceneFor(t, betaOrg, "group", "cidCtxcapBeta==", "Beta group", time.Minute)
	// Bob holds a scene grant in the beta tenant only.
	bob := uuid.NewString()
	if _, err := contextcap.UpsertGrant(ctx, testPool, contextcap.Grant{
		UserID: bob, WorkspaceID: testWorkspaceID, AgentID: agentID, ScopeType: contextcap.ScopeScene, OrgID: betaOrg,
		ScopeKey: betaScene, ScopeTitle: "Beta group", Source: contextcap.GrantSourceAgentLink,
	}, contextcap.GrantTTLScene); err != nil {
		t.Fatal(err)
	}
	// A grant under an org that is not a tenant is ignored everywhere.
	if _, err := contextcap.UpsertGrant(ctx, testPool, contextcap.Grant{
		UserID: bob, WorkspaceID: testWorkspaceID, AgentID: agentID, ScopeType: contextcap.ScopeScene, OrgID: "org-not-tenant",
		ScopeKey: f.sceneFor(t, "org-not-tenant", "group", "cidCtxcapNotTenant==", "", time.Minute), Source: contextcap.GrantSourceAgentLink,
	}, contextcap.GrantTTLScene); err != nil {
		t.Fatal(err)
	}

	w := ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents", bob, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "agents")
	var agents struct {
		Agents []struct {
			ID     string               `json:"id"`
			Scopes []contextCapGrantDTO `json:"scopes"`
		} `json:"agents"`
	}
	ctxcapDecode(t, w, &agents)
	if len(agents.Agents) != 1 || len(agents.Agents[0].Scopes) != 1 || agents.Agents[0].Scopes[0].OrgID != betaOrg {
		t.Fatalf("agents = %+v", agents.Agents)
	}

	// Without org_id Bob lands in the only org he has a grant in.
	type detail struct {
		Tenant  *contextCapTenantRefDTO  `json:"tenant"`
		Tenants []contextCapTenantRefDTO `json:"tenants"`
		Org     *contextCapOrgLayerDTO   `json:"org"`
		Scenes  []contextCapSceneDTO     `json:"scenes"`
		Access  string                   `json:"access"`
	}
	var got detail
	w = ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID, bob, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "bob detail")
	ctxcapDecode(t, w, &got)
	// The enterprise layer is for agent managers only: Bob does not see it.
	if got.Tenant == nil || got.Tenant.OrgID != betaOrg || len(got.Tenants) != 1 || got.Org != nil ||
		len(got.Scenes) != 1 || got.Scenes[0].ScopeKey != betaScene || got.Scenes[0].OrgID != betaOrg {
		t.Fatalf("bob detail = %+v", got)
	}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID+"?org_id="+ctxcapOrg, bob, nil),
		http.StatusForbidden, "bob in the identity org")
	w = ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID+"?org_id=org-not-tenant", bob, nil)
	if w.Code != http.StatusNotFound || catalogErrorCode(t, w) != contextCapErrTenantNotFound {
		t.Fatalf("unknown org: %d %s", w.Code, w.Body.String())
	}
	// Someone without any access to the agent cannot tell tenant org ids
	// from other ones: both are 403.
	outsider := uuid.NewString()
	for _, org := range []string{betaOrg, "org-not-tenant"} {
		ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID+"?org_id="+org, outsider, nil),
			http.StatusForbidden, "outsider detail in "+org)
		ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, betaScene)+"?org_id="+org, outsider, nil),
			http.StatusForbidden, "outsider scene in "+org)
		ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, "/api/context-capabilities/agents/"+agentID+"/bindings", outsider,
			map[string]any{"scope_type": "org", "scope_key": org, "resource_type": "skill", "resource_id": f.skillScene, "enabled": true}),
			http.StatusForbidden, "outsider org binding in "+org)
	}
	// Bob reads his group in the beta org, not without the org.
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, betaScene)+"?org_id="+betaOrg, bob, nil), http.StatusOK, "beta scene")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, betaScene), bob, nil), http.StatusForbidden, "beta scene without org")

	// Bob may not write the enterprise layer; a manager may.
	orgBinding := map[string]any{"scope_type": "org", "scope_key": betaOrg, "resource_type": "skill", "resource_id": f.skillScene, "enabled": true}
	w = ctxcapMobile(t, router, http.MethodPut, "/api/context-capabilities/agents/"+agentID+"/bindings", bob, orgBinding)
	if w.Code != http.StatusForbidden || catalogErrorCode(t, w) != contextCapErrManagerOnly {
		t.Fatalf("bob org binding: %d %s", w.Code, w.Body.String())
	}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, "/api/context-capabilities/agents/"+agentID+"/bindings", testUserID, orgBinding),
		http.StatusOK, "manager org binding")
	orgCredential := map[string]string{"scope_type": "org", "scope_key": betaOrg, "connector_id": f.scene, "bearer": "beta-org-token"}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, "/api/context-capabilities/agents/"+agentID+"/credentials", bob, orgCredential),
		http.StatusForbidden, "bob org credential")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, "/api/context-capabilities/agents/"+agentID+"/credentials", testUserID, orgCredential),
		http.StatusOK, "manager org credential")
	// The binding lands in the beta org scope.
	if stored, err := contextcap.ListScopeBindings(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopeOrg, betaOrg, betaOrg); err != nil ||
		!ctxcapStoredBinding(stored, f.skillScene, true, testUserID) {
		t.Fatalf("beta org bindings = %+v %v", stored, err)
	}
	// A mismatched org_id is an invalid scope.
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, "/api/context-capabilities/agents/"+agentID+"/bindings", testUserID,
		map[string]any{"scope_type": "org", "scope_key": betaOrg, "org_id": ctxcapOrg, "resource_type": "skill", "resource_id": f.skillScene, "enabled": true}),
		http.StatusBadRequest, "org key of another org")

	w = ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID+"?org_id="+betaOrg, bob, nil)
	got.Org = nil
	ctxcapDecode(t, w, &got)
	if got.Org != nil {
		t.Fatalf("bob org layer = %+v, want none", got.Org)
	}
	// The manager sees every tenant, the identity org by default, and the
	// enterprise layer as editable.
	w = ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID, testUserID, nil)
	ctxcapDecode(t, w, &got)
	if got.Access != contextCapAccessManager || got.Tenant == nil || got.Tenant.OrgID != ctxcapOrg || len(got.Tenants) != 2 || got.Org == nil || !got.Org.CanEdit ||
		got.Org.Rights != contextCapAllRights {
		t.Fatalf("manager detail = %+v", got)
	}
	w = ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID+"?org_id="+betaOrg, testUserID, nil)
	ctxcapDecode(t, w, &got)
	if got.Org == nil || len(got.Org.Credentials) != 1 || got.Org.Credentials[0].Hint == "" {
		t.Fatalf("manager beta org layer = %+v", got.Org)
	}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodDelete, "/api/context-capabilities/agents/"+agentID+"/credentials?scope_type=org&scope_key="+betaOrg+"&connector_id="+f.scene, testUserID, nil),
		http.StatusNoContent, "manager removes the org credential")
}

// The group picker of the configure page grants the picked group in the
// tenant the page works in (org_id), against the caller's person grant in
// that tenant.
func TestContextConfigSceneResolveInTenantOrg(t *testing.T) {
	f := newCtxcapFixture(t)
	f.cleanupScenes(t)
	f.cleanupGrantsAndLinks(t)
	router := ctxcapRouter(f.h)
	agentID := uuidToString(f.agent)
	ctx := context.Background()
	const betaOrg, betaCID = "org-ctxcap-beta", "cidCtxcapBetaPicked=="
	if _, err := contextcap.CreateTenant(ctx, testPool, contextcap.TenantWrite{WorkspaceID: testWorkspaceID, AgentID: agentID, OrgID: betaOrg, Name: "Beta"}); err != nil {
		t.Fatal(err)
	}
	f.h.DingTalk = &ctxcapFakeDingTalk{supported: true, ticket: "ticket", chats: map[string]string{"chat-beta": betaCID}}
	betaScene := f.sceneFor(t, betaOrg, "group", betaCID, "Beta group", time.Minute)
	// Alice redeemed a personal link in the beta tenant only.
	alice := uuid.NewString()
	if _, err := contextcap.UpsertGrant(ctx, testPool, contextcap.Grant{
		UserID: alice, WorkspaceID: testWorkspaceID, AgentID: agentID, ScopeType: contextcap.ScopePerson, OrgID: betaOrg,
		ScopeKey: ctxcapStaff, ScopeTitle: "Alice", Source: contextcap.GrantSourceAgentLink,
	}, contextcap.GrantTTLPerson); err != nil {
		t.Fatal(err)
	}
	resolvePath := "/api/context-capabilities/agents/" + agentID + "/scenes/resolve"
	pick := map[string]any{"chat_id": "chat-beta"}

	// Without org_id the picker works in the identity org, where Alice has
	// no person grant.
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPost, resolvePath, alice, pick), http.StatusForbidden, "identity org")
	w := ctxcapMobile(t, router, http.MethodPost, resolvePath+"?org_id=org-not-tenant", alice, pick)
	if w.Code != http.StatusNotFound || catalogErrorCode(t, w) != contextCapErrTenantNotFound {
		t.Fatalf("unknown org: %d %s", w.Code, w.Body.String())
	}
	w = ctxcapMobile(t, router, http.MethodPost, resolvePath+"?org_id="+betaOrg, alice, pick)
	ctxcapExpectStatus(t, w, http.StatusOK, "beta tenant")
	var resolved struct {
		Scene contextCapSceneDTO `json:"scene"`
	}
	ctxcapDecode(t, w, &resolved)
	if resolved.Scene.ScopeKey != betaScene || resolved.Scene.ScopeTitle != "Beta group" || resolved.Scene.OrgID != betaOrg {
		t.Fatalf("resolved scene=%+v", resolved.Scene)
	}
	if _, err := contextcap.GetLiveGrant(ctx, testPool, alice, agentID, contextcap.ScopeScene, betaOrg, betaScene); err != nil {
		t.Fatalf("scene grant in the beta tenant: %v", err)
	}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, betaScene)+"?org_id="+betaOrg, alice, nil),
		http.StatusOK, "picked scene in the beta tenant")
}
