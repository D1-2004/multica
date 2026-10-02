package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/contextcap"
)

const (
	// Scene scope keys are scene ids (docs/agent-scene.md).
	appsSceneEnabled   = "a9a9a9a9-0000-4000-8000-000000000001"
	appsSceneConnected = "a9a9a9a9-0000-4000-8000-000000000002"
	appsSceneOff       = "a9a9a9a9-0000-4000-8000-000000000003"
	appsSceneDirect    = "a9a9a9a9-0000-4000-8000-000000000004"
	appsSceneUnknown   = "a9a9a9a9-0000-4000-8000-0000000000ff"
	appsPersonBoth     = "staff-apps-both"
	appsPersonCred     = "staff-apps-cred"
)

// connectedAppsRouter mirrors the connected-apps, shared-credential and
// mobile wiring in cmd/server/router.go (the workspace admin role check of
// the credential route is router middleware and not repeated here).
func connectedAppsRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.With(RequireHumanActor).Get("/api/agents/{id}/connected-apps", h.ListAgentConnectedApps)
	r.With(RequireHumanActor).Get("/api/agents/{id}/connected-apps/{slug}", h.GetAgentConnectedApp)
	r.With(RequireWorkspaceMCPHumanIssuer).Delete("/api/workspaces/{id}/internal-connectors/{connectorId}/credential", h.DeleteInternalConnectorCredential)
	r.Route("/api/context-capabilities", func(r chi.Router) {
		r.Use(RequireDingTalkHumanActor)
		r.Post("/agents/{agentId}/connections/start", h.StartContextConfigConnection)
	})
	return r
}

type connectedAppsListResponse struct {
	Apps     []connectedAppDTO `json:"apps"`
	CanAdmin bool              `json:"can_admin"`
}

func connectedAppBySlug(t *testing.T, apps []connectedAppDTO, slug string) connectedAppDTO {
	t.Helper()
	for _, app := range apps {
		if app.Slug == slug {
			return app
		}
	}
	t.Fatalf("app %q missing from %+v", slug, apps)
	return connectedAppDTO{}
}

func (f *catalogFixture) listConnectedApps(t *testing.T, router http.Handler, userID string) connectedAppsListResponse {
	t.Helper()
	rec := catalogAdmin(t, router, http.MethodGet, "/api/agents/"+f.agentID+"/connected-apps", userID, nil)
	ctxcapExpectStatus(t, rec, http.StatusOK, "connected apps")
	assertNoTokenMaterial(t, "connected apps", rec.Body.String())
	var out connectedAppsListResponse
	ctxcapDecode(t, rec, &out)
	return out
}

func (f *catalogFixture) connectedApp(t *testing.T, router http.Handler, slug string) connectedAppDetailDTO {
	t.Helper()
	rec := catalogAdmin(t, router, http.MethodGet, "/api/agents/"+f.agentID+"/connected-apps/"+slug, testUserID, nil)
	ctxcapExpectStatus(t, rec, http.StatusOK, "connected app detail")
	assertNoTokenMaterial(t, "connected app detail", rec.Body.String())
	var out connectedAppDetailDTO
	ctxcapDecode(t, rec, &out)
	return out
}

// bindScope writes one scene or person binding of the fixture agent.
func (f *catalogFixture) bindScope(t *testing.T, scopeType, key, title, connectorID string, enabled bool, share *bool) {
	t.Helper()
	if _, err := contextcap.UpsertBinding(context.Background(), testPool, contextcap.BindingWrite{
		WorkspaceID: testWorkspaceID, AgentID: f.agentID, ScopeType: scopeType, OrgID: catalogTestOrg, ScopeKey: key, ScopeTitle: title,
		ResourceType: contextcap.ResourceConnector, ResourceID: connectorID, Enabled: enabled, ShareInGroups: share,
	}); err != nil {
		t.Fatal(err)
	}
}

// storeScopeCredential stores a scene or person credential: an OAuth
// account when account is set, else a pasted token.
func (f *catalogFixture) storeScopeCredential(t *testing.T, scopeType, orgID, key, connectorID, account string) {
	t.Helper()
	binding := contextcap.CredentialBinding{WorkspaceID: testWorkspaceID, AgentID: f.agentID, ConnectorID: connectorID,
		ScopeType: scopeType, OrgID: orgID, ScopeKey: key}
	var sealed []byte
	var err error
	hint := contextcap.Hint("pat-scope-token-1234")
	if account != "" {
		sealed, err = contextcap.SealOAuthCredential(f.box, binding, contextcap.OAuthToken{AccessToken: "acc-scope-" + key, Account: account})
		hint = contextcap.OAuthHint(account)
	} else {
		sealed, err = contextcap.SealCredential(f.box, binding, "pat-scope-token-1234")
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := contextcap.UpsertCredential(context.Background(), testPool, binding, sealed, hint, testUserID); err != nil {
		t.Fatal(err)
	}
}

func (f *catalogFixture) cleanupAppScenes(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		bg := context.Background()
		for _, statement := range []string{
			`DELETE FROM scene_memory WHERE agent_id = $1`,
			`DELETE FROM agent_scene_config WHERE agent_id = $1`,
			`DELETE FROM context_connector_credential WHERE agent_id = $1`,
		} {
			_, _ = testPool.Exec(bg, statement, f.agentID)
		}
	})
}

func TestAgentConnectedAppsStatusAndUsage(t *testing.T) {
	f := newCatalogFixture(t)
	f.cleanupAppScenes(t)
	t.Setenv("GITHUB_APP_SLUG", "multica-test-app")
	router := connectedAppsRouter(f.h)
	ctx := context.Background()

	// Every catalog app is listed, even before anything is added.
	list := f.listConnectedApps(t, router, testUserID)
	if !list.CanAdmin || len(list.Apps) != 2 || list.Apps[0].Slug != f.dcr.Slug || list.Apps[1].Slug != f.gh.Slug {
		t.Fatalf("initial list = %+v", list)
	}
	dcrApp, ghApp := list.Apps[0], list.Apps[1]
	if dcrApp.ConnectorID != nil || dcrApp.Added || dcrApp.GlobalEnabled || dcrApp.Offered || dcrApp.EnabledInWorkspace || dcrApp.SharedAccount.Connected ||
		dcrApp.AuthKind != "oauth_dcr" || !dcrApp.OAuthAvailable || dcrApp.AllowsPAT || dcrApp.InstallURL != "" || dcrApp.Usage != (connectedAppUsageDTO{}) {
		t.Fatalf("unadded DCR app = %+v", dcrApp)
	}
	if ghApp.AuthKind != "oauth_github_app" || !ghApp.OAuthAvailable || !ghApp.AllowsPAT ||
		ghApp.InstallURL != "https://github.com/apps/multica-test-app/installations/new" {
		t.Fatalf("unadded GitHub app = %+v", ghApp)
	}
	rec := catalogAdmin(t, router, http.MethodGet, "/api/agents/"+f.agentID+"/connected-apps", testUserID, nil)
	if !strings.Contains(rec.Body.String(), `"connector_id":null`) || !strings.Contains(rec.Body.String(), `"install_url":""`) ||
		!strings.Contains(rec.Body.String(), `"shared_account":{"connected":false,"account":"","source":""}`) {
		t.Fatalf("list JSON shape: %s", rec.Body.String())
	}

	// Added to the workspace only: the connector exists, the agent does not
	// use it yet.
	dcr := f.create(t, f.dcr)
	dcrApp = connectedAppBySlug(t, f.listConnectedApps(t, router, testUserID).Apps, f.dcr.Slug)
	if dcrApp.ConnectorID == nil || *dcrApp.ConnectorID != dcr.ID || dcrApp.Added || !dcrApp.EnabledInWorkspace || dcrApp.GlobalEnabled || dcrApp.Offered {
		t.Fatalf("workspace-only DCR app = %+v", dcrApp)
	}

	// Offered, granted, tools, shared account.
	f.grantGlobally(t, dcr.ID)
	f.offer(t, dcr.ID)
	f.storeTools(t, dcr)
	f.sealWorkspaceOAuth(t, dcr.ID, contextcap.OAuthToken{AccessToken: "acc-shared-token", RefreshToken: "ref-shared", ExpiresAt: time.Now().Add(-time.Hour).Unix(), Account: "octo"})

	// Scene and person uses under the agent's org, on the agent's scenes.
	registerFixedScene(t, f.agentID, catalogTestOrg, "group", appsSceneEnabled, "cidAppsEnabled==", "Memory title", 3*time.Minute)
	registerFixedScene(t, f.agentID, catalogTestOrg, "group", appsSceneOff, "cidAppsOff==", "Off group", 3*time.Minute)
	registerFixedScene(t, f.agentID, catalogTestOrg, "dm", appsSceneDirect, "cidAppsDirect==", "Direct chat", 2*time.Minute)
	registerFixedScene(t, f.agentID, catalogTestOrg, "group", appsSceneConnected, "cidAppsConnected==", "Connected group", time.Minute)
	share := true
	f.bindScope(t, contextcap.ScopeScene, appsSceneEnabled, "Enabled group", dcr.ID, true, nil)
	f.storeScopeCredential(t, contextcap.ScopeScene, catalogTestOrg, appsSceneConnected, dcr.ID, "")
	f.bindScope(t, contextcap.ScopeScene, appsSceneOff, "Off group", dcr.ID, false, nil)
	f.bindScope(t, contextcap.ScopePerson, appsPersonBoth, "Bob", dcr.ID, true, &share)
	f.storeScopeCredential(t, contextcap.ScopePerson, catalogTestOrg, appsPersonBoth, dcr.ID, "bob-gh")
	f.storeScopeCredential(t, contextcap.ScopePerson, catalogTestOrg, appsPersonCred, dcr.ID, "alice")
	// Another org's rows never count.
	f.storeScopeCredential(t, contextcap.ScopePerson, "org-previous", appsPersonCred, dcr.ID, "old")
	if _, err := contextcap.UpsertGrant(ctx, testPool, contextcap.Grant{UserID: uuid.NewString(), WorkspaceID: testWorkspaceID, AgentID: f.agentID,
		ScopeType: contextcap.ScopePerson, OrgID: catalogTestOrg, ScopeKey: appsPersonCred, ScopeTitle: "Alice", Source: contextcap.GrantSourceAgentLink}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_scene_memory (scene_id, workspace_id, agent_id, memory_text)
		VALUES ($1, $2, $3, 'notes')`, appsSceneEnabled, testWorkspaceID, f.agentID); err != nil {
		t.Fatal(err)
	}
	// A 1:1 chat is a scene like a group: its own configuration counts.
	f.bindScope(t, contextcap.ScopeScene, appsSceneDirect, "Direct chat", dcr.ID, true, nil)
	f.storeScopeCredential(t, contextcap.ScopeScene, catalogTestOrg, appsSceneDirect, dcr.ID, "")

	dcrApp = connectedAppBySlug(t, f.listConnectedApps(t, router, testUserID).Apps, f.dcr.Slug)
	wantUsage := connectedAppUsageDTO{ScenesEnabled: 2, ScenesConnected: 2, PersonsEnabled: 1, PersonsConnected: 2}
	if !dcrApp.Added || !dcrApp.GlobalEnabled || !dcrApp.Offered || dcrApp.WriteEnabled || dcrApp.Usage != wantUsage ||
		dcrApp.Tools != (connectedAppToolsDTO{Discovered: 2, Allowed: 1, ReadOnly: 1}) {
		t.Fatalf("configured DCR app = %+v", dcrApp)
	}
	// An expired OAuth token with a refresh token is still usable.
	if dcrApp.SharedAccount != (connectedAppSharedAccountDTO{Connected: true, Account: "@octo", Source: "workspace"}) {
		t.Fatalf("shared account = %+v", dcrApp.SharedAccount)
	}

	detail := f.connectedApp(t, router, f.dcr.Slug)
	if detail.connectedAppDTO.Usage != wantUsage || !detail.CanAdmin || len(detail.ToolList) != 2 ||
		detail.ToolList[0] != (connectedAppToolDTO{Name: "search", ReadOnly: true, Allowed: true}) ||
		detail.ToolList[1] != (connectedAppToolDTO{Name: "create_issue", ReadOnly: false, Allowed: false}) {
		t.Fatalf("detail = %+v", detail)
	}
	scenes := map[string]connectedAppSceneDTO{}
	for _, scene := range detail.Scenes {
		scenes[scene.SceneKey] = scene
	}
	if len(scenes) != 3 {
		t.Fatalf("detail scenes = %+v, want the enabled, the 1:1 and the connected scene", detail.Scenes)
	}
	// Newest scene activity first.
	if detail.Scenes[0].SceneKey != appsSceneConnected || detail.Scenes[1].SceneKey != appsSceneDirect || detail.Scenes[2].SceneKey != appsSceneEnabled {
		t.Fatalf("detail scene order = %+v", detail.Scenes)
	}
	if got := scenes[appsSceneEnabled]; got != (connectedAppSceneDTO{SceneKey: appsSceneEnabled, SceneID: appsSceneEnabled, Title: "Memory title", Kind: "group", Enabled: true}) {
		t.Fatalf("enabled scene = %+v", got)
	}
	if got := scenes[appsSceneDirect]; got.Kind != "dm" || !got.Enabled || !got.Connected {
		t.Fatalf("1:1 scene = %+v", got)
	}
	if got := scenes[appsSceneConnected]; got.Title != "Connected group" || got.Kind != "group" || got.Enabled || !got.Connected || !strings.HasPrefix(got.Account, "••••") {
		t.Fatalf("connected scene = %+v", got)
	}
	if len(detail.Persons) != 2 {
		t.Fatalf("detail persons = %+v", detail.Persons)
	}
	if got := detail.Persons[0]; got != (connectedAppPersonDTO{ScopeKey: appsPersonCred, Title: "Alice", Connected: true, Account: "@alice"}) {
		t.Fatalf("credential-only person = %+v", got)
	}
	if got := detail.Persons[1]; got != (connectedAppPersonDTO{ScopeKey: appsPersonBoth, Title: "Bob", Enabled: true, Connected: true, Account: "@bob-gh", ShareInGroups: true}) {
		t.Fatalf("enabled and connected person = %+v", got)
	}

	// Without the offer, bindings stay stored but are not "enabled";
	// stored credentials still count as connected.
	f.offer(t)
	dcrApp = connectedAppBySlug(t, f.listConnectedApps(t, router, testUserID).Apps, f.dcr.Slug)
	if dcrApp.Offered || !dcrApp.Added || dcrApp.Usage != (connectedAppUsageDTO{ScenesConnected: 2, PersonsConnected: 2}) {
		t.Fatalf("unoffered DCR app = %+v", dcrApp)
	}
	detail = f.connectedApp(t, router, f.dcr.Slug)
	if len(detail.Scenes) != 2 || detail.Scenes[0].SceneKey != appsSceneConnected || detail.Scenes[1].SceneKey != appsSceneDirect ||
		detail.Scenes[0].Enabled || detail.Scenes[1].Enabled || len(detail.Persons) != 2 || detail.Persons[1].Enabled {
		t.Fatalf("unoffered detail = %+v", detail)
	}
	// Removed from the agent (no grant, no offer): not added, the workspace
	// connector stays.
	if _, err := testPool.Exec(ctx, `DELETE FROM internal_connector_agent WHERE connector_id = $1 AND agent_id = $2`, dcr.ID, f.agentID); err != nil {
		t.Fatal(err)
	}
	dcrApp = connectedAppBySlug(t, f.listConnectedApps(t, router, testUserID).Apps, f.dcr.Slug)
	if dcrApp.Added || dcrApp.GlobalEnabled || dcrApp.ConnectorID == nil {
		t.Fatalf("removed DCR app = %+v", dcrApp)
	}

	// A workspace-disabled connector, and an expired OAuth token without a
	// refresh token: not connected.
	if _, err := testPool.Exec(ctx, `UPDATE internal_connector SET enabled = false WHERE id = $1`, dcr.ID); err != nil {
		t.Fatal(err)
	}
	f.sealWorkspaceOAuth(t, dcr.ID, contextcap.OAuthToken{AccessToken: "acc-expired", ExpiresAt: time.Now().Add(-time.Hour).Unix(), Account: "octo"})
	dcrApp = connectedAppBySlug(t, f.listConnectedApps(t, router, testUserID).Apps, f.dcr.Slug)
	if dcrApp.EnabledInWorkspace || dcrApp.SharedAccount != (connectedAppSharedAccountDTO{}) {
		t.Fatalf("disabled DCR app with an expired token = %+v", dcrApp)
	}

	// A pasted GitHub PAT as the shared account.
	gh := f.create(t, f.gh)
	sealed, err := f.h.sealWorkspaceConnectorSecret(testWorkspaceID, gh.ID, contextcap.Secret{Bearer: "pat-workspace-token-9876"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE internal_connector SET credential_ciphertext = $2 WHERE id = $1`, gh.ID, sealed); err != nil {
		t.Fatal(err)
	}
	ghApp = connectedAppBySlug(t, f.listConnectedApps(t, router, testUserID).Apps, f.gh.Slug)
	if ghApp.SharedAccount != (connectedAppSharedAccountDTO{Connected: true, Account: "••••9876", Source: "workspace"}) {
		t.Fatalf("GitHub PAT shared account = %+v", ghApp.SharedAccount)
	}

	// Disconnecting the shared account: 204, idempotent, 404 for an unknown
	// connector, 400 for a malformed one.
	credentialPath := "/api/workspaces/" + testWorkspaceID + "/internal-connectors/" + gh.ID + "/credential"
	ctxcapExpectStatus(t, catalogAdmin(t, router, http.MethodDelete, credentialPath, testUserID, nil), http.StatusNoContent, "disconnect shared account")
	ghApp = connectedAppBySlug(t, f.listConnectedApps(t, router, testUserID).Apps, f.gh.Slug)
	if ghApp.SharedAccount.Connected {
		t.Fatalf("shared account after disconnect = %+v", ghApp.SharedAccount)
	}
	ctxcapExpectStatus(t, catalogAdmin(t, router, http.MethodDelete, credentialPath, testUserID, nil), http.StatusNoContent, "disconnect again")
	ctxcapExpectStatus(t, catalogAdmin(t, router, http.MethodDelete, "/api/workspaces/"+testWorkspaceID+"/internal-connectors/"+uuid.NewString()+"/credential", testUserID, nil),
		http.StatusNotFound, "disconnect unknown connector")
	ctxcapExpectStatus(t, catalogAdmin(t, router, http.MethodDelete, "/api/workspaces/"+testWorkspaceID+"/internal-connectors/nope/credential", testUserID, nil),
		http.StatusBadRequest, "disconnect malformed connector")
	ctxcapExpectStatus(t, catalogAdmin(t, router, http.MethodDelete, credentialPath, testUserID, nil, "X-Actor-Source", "task_token"),
		http.StatusForbidden, "disconnect with a task token")

	// An operator-managed environment credential serves runs, so it is
	// connected, but it is neither hinted nor removable from the page.
	t.Setenv(connectorCredentialRef(gh.ID), "env-operator-token-4321")
	ghApp = connectedAppBySlug(t, f.listConnectedApps(t, router, testUserID).Apps, f.gh.Slug)
	if ghApp.SharedAccount != (connectedAppSharedAccountDTO{Connected: true, Source: "environment"}) {
		t.Fatalf("environment shared account = %+v", ghApp.SharedAccount)
	}
	rec = catalogAdmin(t, router, http.MethodGet, "/api/agents/"+f.agentID+"/connected-apps/"+f.gh.Slug, testUserID, nil)
	if strings.Contains(rec.Body.String(), "4321") || strings.Contains(rec.Body.String(), "env-operator") {
		t.Fatalf("environment credential leaked: %s", rec.Body.String())
	}

	// Unknown app.
	ctxcapExpectStatus(t, catalogAdmin(t, router, http.MethodGet, "/api/agents/"+f.agentID+"/connected-apps/not-an-app", testUserID, nil), http.StatusNotFound, "unknown app")
}

func TestAgentConnectedAppsPermissionAndOAuthAvailability(t *testing.T) {
	f := newCatalogFixture(t)
	router := connectedAppsRouter(f.h)
	listPath := "/api/agents/" + f.agentID + "/connected-apps"

	owner := createPermissionTestMember(t, "apps-owner-"+uuid.NewString()[:8]+"@example.test")
	member := createPermissionTestMember(t, "apps-member-"+uuid.NewString()[:8]+"@example.test")
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET owner_id = $1 WHERE id = $2`, owner, f.agentID); err != nil {
		t.Fatal(err)
	}
	// The agent owner (a plain member) reads the page but cannot administer
	// the workspace library; a plain member and outsiders get nothing.
	if list := f.listConnectedApps(t, router, owner); list.CanAdmin || len(list.Apps) != 2 {
		t.Fatalf("agent owner list = %+v", list)
	}
	rec := catalogAdmin(t, router, http.MethodGet, listPath+"/"+f.dcr.Slug, owner, nil)
	ctxcapExpectStatus(t, rec, http.StatusOK, "agent owner detail")
	var detail connectedAppDetailDTO
	ctxcapDecode(t, rec, &detail)
	if detail.CanAdmin || detail.Slug != f.dcr.Slug {
		t.Fatalf("agent owner detail = %+v", detail)
	}
	// The connector library is admin-only: an app in the workspace that is
	// neither granted nor offered here shows only catalog facts to the
	// agent owner, and the shared account never carries its hint.
	gh := f.create(t, f.gh)
	f.storeTools(t, gh)
	sealed, err := f.h.sealWorkspaceConnectorSecret(testWorkspaceID, gh.ID, contextcap.Secret{Bearer: "pat-workspace-token-9876"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE internal_connector SET credential_ciphertext = $2 WHERE id = $1`, gh.ID, sealed); err != nil {
		t.Fatal(err)
	}
	libraryOnly := connectedAppBySlug(t, f.listConnectedApps(t, router, owner).Apps, f.gh.Slug)
	if libraryOnly.ConnectorID != nil || libraryOnly.Added || libraryOnly.EnabledInWorkspace || libraryOnly.WriteEnabled ||
		libraryOnly.Tools != (connectedAppToolsDTO{}) || libraryOnly.SharedAccount != (connectedAppSharedAccountDTO{}) {
		t.Fatalf("library-only app for the agent owner = %+v", libraryOnly)
	}
	rec = catalogAdmin(t, router, http.MethodGet, listPath+"/"+f.gh.Slug, owner, nil)
	ctxcapExpectStatus(t, rec, http.StatusOK, "agent owner, library-only detail")
	if strings.Contains(rec.Body.String(), gh.ID) || strings.Contains(rec.Body.String(), "9876") {
		t.Fatalf("library-only detail leaked the connector: %s", rec.Body.String())
	}
	var ownerDetail connectedAppDetailDTO
	ctxcapDecode(t, rec, &ownerDetail)
	if ownerDetail.ConnectorID != nil || len(ownerDetail.ToolList) != 0 {
		t.Fatalf("library-only detail for the agent owner = %+v", ownerDetail)
	}
	// The admin sees the library connector with the hint.
	if admin := connectedAppBySlug(t, f.listConnectedApps(t, router, testUserID).Apps, f.gh.Slug); admin.ConnectorID == nil || admin.SharedAccount.Account != "••••9876" {
		t.Fatalf("library-only app for the admin = %+v", admin)
	}
	// Once granted, the owner sees it, connected but without the hint.
	f.grantGlobally(t, gh.ID)
	granted := connectedAppBySlug(t, f.listConnectedApps(t, router, owner).Apps, f.gh.Slug)
	if granted.ConnectorID == nil || !granted.GlobalEnabled ||
		granted.SharedAccount != (connectedAppSharedAccountDTO{Connected: true, Source: "workspace"}) {
		t.Fatalf("granted app for the agent owner = %+v", granted)
	}

	ctxcapExpectStatus(t, catalogAdmin(t, router, http.MethodGet, listPath, member, nil), http.StatusForbidden, "plain member")
	ctxcapExpectStatus(t, catalogAdmin(t, router, http.MethodGet, listPath+"/"+f.dcr.Slug, member, nil), http.StatusForbidden, "plain member detail")
	ctxcapExpectStatus(t, catalogAdmin(t, router, http.MethodGet, listPath, uuid.NewString(), nil), http.StatusNotFound, "outsider")
	ctxcapExpectStatus(t, catalogAdmin(t, router, http.MethodGet, listPath, testUserID, nil, "X-Actor-Source", "task_token"), http.StatusForbidden, "task token")

	// oauth_available mirrors every deployment check of the start endpoint.
	t.Setenv("GITHUB_APP_CLIENT_SECRET", "")
	list := f.listConnectedApps(t, router, testUserID)
	if !connectedAppBySlug(t, list.Apps, f.dcr.Slug).OAuthAvailable || connectedAppBySlug(t, list.Apps, f.gh.Slug).OAuthAvailable {
		t.Fatalf("oauth_available without the GitHub App secret = %+v", list.Apps)
	}
	noOrigin := *f.h
	noOrigin.cfg.AppURL, noOrigin.cfg.FrontendOrigin = "", ""
	for _, app := range f.listConnectedApps(t, connectedAppsRouter(&noOrigin), testUserID).Apps {
		if app.OAuthAvailable {
			t.Fatalf("oauth_available without an app origin = %+v", app)
		}
	}
	noBox := *f.h
	noBox.InternalConnectorSecretBox = nil
	for _, app := range f.listConnectedApps(t, connectedAppsRouter(&noBox), testUserID).Apps {
		// Without credential storage a Personal Access Token cannot be
		// saved either (PUT .../credential answers 503).
		if app.OAuthAvailable || app.AllowsPAT {
			t.Fatalf("oauth_available / allows_pat without credential storage = %+v", app)
		}
	}
	if !connectedAppBySlug(t, f.listConnectedApps(t, router, testUserID).Apps, f.gh.Slug).AllowsPAT {
		t.Fatal("allows_pat with credential storage")
	}
}

// A manager of the agent connects a scene's account from the configure page
// without a scene grant; the callback re-checks the same authority.
func TestContextConfigConnectionStartAcceptsManagerForScenes(t *testing.T) {
	f := newCatalogFixture(t)
	f.cleanupAppScenes(t)
	router := connectedAppsRouter(f.h)
	ctx := context.Background()
	dcr := f.create(t, f.dcr)
	f.offer(t, dcr.ID)
	// catalogTestScene is a group scene of the agent (the fixture registers it).
	startPath := "/api/context-capabilities/agents/" + f.agentID + "/connections/start"
	start := func(userID, scopeType, key string) *httptest.ResponseRecorder {
		return ctxcapMobile(t, router, http.MethodPost, startPath, userID, map[string]string{"scope_type": scopeType, "scope_key": key, "connector_id": dcr.ID})
	}
	member := createPermissionTestMember(t, "apps-connect-"+uuid.NewString()[:8]+"@example.test")

	// testUserID owns the workspace and holds no grant.
	f.takeAuthorizeURL(t, start(testUserID, contextcap.ScopeScene, catalogTestScene))
	ctxcapExpectStatus(t, start(testUserID, contextcap.ScopeScene, uuid.NewString()), http.StatusNotFound, "manager, unknown scene")
	ctxcapExpectStatus(t, start(testUserID, contextcap.ScopePerson, catalogTestStaff), http.StatusForbidden, "manager, person scope")
	ctxcapExpectStatus(t, start(member, contextcap.ScopeScene, catalogTestScene), http.StatusForbidden, "plain member")

	forbidden := func(err error) bool {
		var oauthErr *connectorOAuthError
		return errors.As(err, &oauthErr) && oauthErr.Status == http.StatusForbidden
	}
	c, err := f.h.loadInternalConnector(ctx, testWorkspaceID, dcr.ID)
	if err != nil {
		t.Fatal(err)
	}
	scene := f.scope(dcr.ID, contextcap.ScopeScene, catalogTestScene)
	if err := f.h.authorizeConnectorOAuthScope(ctx, scene, c); err != nil {
		t.Fatalf("callback re-check for a manager: %v", err)
	}
	unknown := f.scope(dcr.ID, contextcap.ScopeScene, appsSceneUnknown)
	if err := f.h.authorizeConnectorOAuthScope(ctx, unknown, c); !forbidden(err) {
		t.Fatalf("callback re-check, unknown scene: %v", err)
	}
	plain := scene
	plain.UserID = member
	if err := f.h.authorizeConnectorOAuthScope(ctx, plain, c); !forbidden(err) {
		t.Fatalf("callback re-check, plain member: %v", err)
	}
	// The enterprise (org) scope of a tenant: managers connect its account;
	// the state names the tenant org, and the callback re-checks that the
	// org is still a tenant.
	f.takeAuthorizeURL(t, start(testUserID, contextcap.ScopeOrg, catalogTestOrg))
	ctxcapExpectStatus(t, start(member, contextcap.ScopeOrg, catalogTestOrg), http.StatusForbidden, "plain member, org scope")
	orgScope := f.scope(dcr.ID, contextcap.ScopeOrg, catalogTestOrg)
	if err := f.h.authorizeConnectorOAuthScope(ctx, orgScope, c); err != nil {
		t.Fatalf("callback re-check, org scope: %v", err)
	}
	if _, err := contextcap.CreateTenant(ctx, testPool, contextcap.TenantWrite{WorkspaceID: testWorkspaceID, AgentID: f.agentID, OrgID: "org-catalog-b", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_tenant WHERE agent_id = $1`, f.agentID)
	})
	tenantScope := orgScope
	tenantScope.OrgID, tenantScope.ScopeKey = "org-catalog-b", "org-catalog-b"
	w := ctxcapMobile(t, router, http.MethodPost, startPath, testUserID, map[string]string{"scope_type": contextcap.ScopeOrg, "scope_key": "org-catalog-b", "connector_id": dcr.ID})
	f.takeAuthorizeURL(t, w)
	var stored string
	if err := testPool.QueryRow(ctx, `SELECT org_id FROM connector_oauth_state WHERE agent_id = $1 AND scope_type = 'org' ORDER BY created_at DESC LIMIT 1`, f.agentID).Scan(&stored); err != nil || stored != "org-catalog-b" {
		t.Fatalf("org connect state org=%q err=%v", stored, err)
	}
	if err := f.h.authorizeConnectorOAuthScope(ctx, tenantScope, c); err != nil {
		t.Fatalf("callback re-check, tenant org scope: %v", err)
	}
	if _, err := testPool.Exec(ctx, `DELETE FROM agent_tenant WHERE agent_id = $1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if err := f.h.authorizeConnectorOAuthScope(ctx, tenantScope, c); !forbidden(err) {
		t.Fatalf("callback re-check after the tenant was deleted: %v", err)
	}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPost, startPath, testUserID,
		map[string]string{"scope_type": contextcap.ScopeOrg, "scope_key": "org-catalog-b", "connector_id": dcr.ID}), http.StatusNotFound, "org of a deleted tenant")
	// The offer gate still applies to managers.
	f.offer(t)
	if err := f.h.authorizeConnectorOAuthScope(ctx, scene, c); !forbidden(err) {
		t.Fatalf("callback re-check without the offer: %v", err)
	}
	if err := f.h.authorizeConnectorOAuthScope(ctx, orgScope, c); !forbidden(err) {
		t.Fatalf("callback re-check without the offer, org scope: %v", err)
	}
}
