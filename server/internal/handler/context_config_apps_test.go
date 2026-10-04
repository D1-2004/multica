package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/connectorcatalog"
	"github.com/multica-ai/multica/server/internal/connectorconfig"
	"github.com/multica-ai/multica/server/internal/contextcap"
)

func ctxcapAppsRouter(h *Handler) http.Handler {
	router := chi.NewRouter()
	router.Route("/api/context-capabilities", func(r chi.Router) {
		r.Use(RequireDingTalkHumanActor)
		r.Get("/agents/{agentId}", h.GetContextConfigAgent)
		r.Post("/agents/{agentId}/apps/{slug}", h.AddContextConfigApp)
		r.Get("/agents/{agentId}/apps/{slug}/oauth-app", h.GetContextConfigOAuthApp)
		r.Put("/agents/{agentId}/apps/{slug}/oauth-app", h.PutContextConfigOAuthApp)
		r.Delete("/agents/{agentId}/apps/{slug}/oauth-app", h.DeleteContextConfigOAuthApp)
		r.Get("/agents/{agentId}/scenes/{sceneKey}", h.GetContextConfigScene)
		r.Post("/agents/{agentId}/connections/start", h.StartContextConfigConnection)
	})
	return router
}

// ctxcapCatalogConnectors lists the workspace's catalog connectors of slug,
// so a test removes only the ones it created.
func ctxcapCatalogConnectors(t *testing.T, slug string) map[string]bool {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT id::text FROM internal_connector WHERE workspace_id = $1 AND catalog_slug = $2`, testWorkspaceID, slug)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out[id] = true
	}
	return out
}

// connectorApps lists the workspace's OAuth application ids of provider, so
// a test removes only the ones it created.
func ctxcapConnectorApps(t *testing.T, provider string) map[string]bool {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT id::text FROM connector_app WHERE workspace_id = $1 AND provider = $2`, testWorkspaceID, provider)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out[id] = true
	}
	return out
}

// A scene's link holder adds an official app nobody opened for the agent:
// the workspace installs it, the agent offers it, and the level switches it
// on. The enterprise level stays with its managers, and an app an admin
// switched off is refused.
func TestContextConfigAddsAnOfficialAppAtALevel(t *testing.T) {
	f := newCtxcapFixture(t)
	router := ctxcapAppsRouter(f.h)
	agentID := uuidToString(f.agent)
	holder, stranger := uuid.NewString(), uuid.NewString()
	f.grant(t, holder, contextcap.ScopeScene, ctxcapScene, "Ctxcap group")
	before := ctxcapCatalogConnectors(t, "notion")
	t.Cleanup(func() {
		for id := range ctxcapCatalogConnectors(t, "notion") {
			if !before[id] {
				_, _ = testPool.Exec(context.Background(), `DELETE FROM internal_connector WHERE id = $1`, id)
			}
		}
	})
	path := "/api/context-capabilities/agents/" + agentID + "/apps/"
	body := map[string]any{"scope_type": "scene", "scope_key": ctxcapScene}

	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPost, path+"notion", stranger, body), http.StatusForbidden, "stranger add")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPost, path+"no-such-app", holder, body), http.StatusNotFound, "unknown app")
	// The enterprise level: its managers only.
	w := ctxcapMobile(t, router, http.MethodPost, path+"notion", holder, map[string]any{"scope_type": "org", "scope_key": ctxcapOrg})
	if w.Code != http.StatusForbidden || catalogErrorCode(t, w) != contextCapErrManagerOnly {
		t.Fatalf("holder add at the enterprise level: %d %s", w.Code, w.Body.String())
	}

	w = ctxcapMobile(t, router, http.MethodPost, path+"notion", holder, body)
	ctxcapExpectStatus(t, w, http.StatusOK, "holder add")
	var added struct {
		ConnectorID string `json:"connector_id"`
		DefaultOn   bool   `json:"default_on"`
	}
	ctxcapDecode(t, w, &added)
	if added.ConnectorID == "" || added.DefaultOn {
		t.Fatalf("added = %+v", added)
	}
	ctx := context.Background()
	if offered, err := contextcap.IsOffered(ctx, testPool, testWorkspaceID, agentID, contextcap.ResourceConnector, added.ConnectorID); err != nil || !offered {
		t.Fatalf("offered = %v %v", offered, err)
	}
	bindings, err := contextcap.ListScopeBindings(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopeScene, ctxcapOrg, ctxcapScene)
	if err != nil || !ctxcapStoredBinding(bindings, added.ConnectorID, true, holder) {
		t.Fatalf("scene bindings = %+v %v", bindings, err)
	}
	// Adding again switches on the offered connector, installing nothing.
	w = ctxcapMobile(t, router, http.MethodPost, path+"notion", holder, body)
	ctxcapExpectStatus(t, w, http.StatusOK, "add again")
	var again struct {
		ConnectorID string `json:"connector_id"`
	}
	ctxcapDecode(t, w, &again)
	if again.ConnectorID != added.ConnectorID {
		t.Fatalf("second add installed %s, want %s", again.ConnectorID, added.ConnectorID)
	}
	// The detail now offers it at every level.
	w = ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID, holder, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "detail")
	if !strings.Contains(w.Body.String(), added.ConnectorID) {
		t.Fatalf("detail does not list the added app: %s", w.Body.String())
	}

	// An admin switched it off: adding says so instead of a silent no-op.
	if _, err := testPool.Exec(ctx, `UPDATE internal_connector SET enabled = false WHERE id = $1`, added.ConnectorID); err != nil {
		t.Fatal(err)
	}
	w = ctxcapMobile(t, router, http.MethodPost, path+"notion", holder, body)
	if w.Code != http.StatusConflict || catalogErrorCode(t, w) != contextConfigErrAppDisabled {
		t.Fatalf("add a disabled app: %d %s", w.Code, w.Body.String())
	}
}

// A scene takes its own OAuth application of an app without dynamic
// registration on the configure page: its members save the first one, only
// the agent's managers change it, and the workspace's application is not
// touched (it stays in the admin console). Secrets never come back.
func TestContextConfigSceneOAuthApplication(t *testing.T) {
	f := newCtxcapFixture(t)
	// A sign-in needs the app origin it returns to.
	f.h.cfg.AppURL = "https://app.multica.example"
	router := ctxcapAppsRouter(f.h)
	agentID := uuidToString(f.agent)
	holder, stranger := uuid.NewString(), uuid.NewString()
	f.grant(t, holder, contextcap.ScopeScene, ctxcapScene, "Ctxcap group")
	if len(ctxcapConnectorApps(t, "slack")) > 0 {
		t.Skip("the test workspace already has a Slack OAuth application")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM context_connector_app WHERE agent_id = $1`, agentID)
	})
	path := "/api/context-capabilities/agents/" + agentID + "/apps/"
	sceneQuery := "?scope_type=scene&scope_key=" + ctxcapScene
	sceneBody := func(extra map[string]any) map[string]any {
		body := map[string]any{"scope_type": "scene", "scope_key": ctxcapScene}
		for k, v := range extra {
			body[k] = v
		}
		return body
	}

	// The workspace's and the enterprise's applications are the admin console's.
	for _, query := range []string{"", "?scope_type=org&scope_key=" + ctxcapOrg, "?scope_type=person&scope_key=staff"} {
		w := ctxcapMobile(t, router, http.MethodGet, path+"slack/oauth-app"+query, testUserID, nil)
		if w.Code != http.StatusBadRequest || catalogErrorCode(t, w) != contextConfigErrOAuthAppSceneOnly {
			t.Fatalf("GET %q: %d %s", query, w.Code, w.Body.String())
		}
	}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodGet, path+"slack/oauth-app"+sceneQuery, stranger, nil), http.StatusForbidden, "stranger read")
	// A dynamic-registration app needs none.
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodGet, path+"notion/oauth-app"+sceneQuery, holder, nil), http.StatusNotFound, "notion")

	w := ctxcapMobile(t, router, http.MethodGet, path+"slack/oauth-app"+sceneQuery, holder, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "holder read")
	var view contextConfigOAuthAppView
	ctxcapDecode(t, w, &view)
	if view.CallbackURL != connectorcatalog.ProductionCallbackURL || len(view.Fields) != 2 || view.Saved || view.Ready || view.WorkspaceReady || !view.CanEdit {
		t.Fatalf("view = %+v", view)
	}
	w = ctxcapMobile(t, router, http.MethodPut, path+"slack/oauth-app", holder, sceneBody(map[string]any{"client_id": "ctxcap-scene-client"}))
	if w.Code != http.StatusBadRequest || catalogErrorCode(t, w) != "oauth_app_field_required" {
		t.Fatalf("save without the secret: %d %s", w.Code, w.Body.String())
	}

	// The first one: a member of the scene fills it in.
	w = ctxcapMobile(t, router, http.MethodPut, path+"slack/oauth-app", holder,
		sceneBody(map[string]any{"client_id": "ctxcap-scene-client", "client_secret": "ctxcap-scene-secret"}))
	ctxcapExpectStatus(t, w, http.StatusOK, "holder saves the first one")
	if strings.Contains(w.Body.String(), "ctxcap-scene-secret") {
		t.Fatalf("the secret came back: %s", w.Body.String())
	}
	ctxcapDecode(t, w, &view)
	if !view.Saved || !view.Ready || view.ClientID != "ctxcap-scene-client" || !view.ClientSecretSet || view.CanEdit {
		t.Fatalf("saved view = %+v", view)
	}
	// The workspace's application is not written.
	if apps := ctxcapConnectorApps(t, "slack"); len(apps) != 0 {
		t.Fatalf("workspace Slack applications = %v", apps)
	}

	// Changing it: the agent's managers only.
	w = ctxcapMobile(t, router, http.MethodPut, path+"slack/oauth-app", holder, sceneBody(map[string]any{"client_id": "other", "client_secret": "other"}))
	if w.Code != http.StatusForbidden || catalogErrorCode(t, w) != contextConfigErrOAuthAppLocked {
		t.Fatalf("holder change: %d %s", w.Code, w.Body.String())
	}
	// A new client ID needs its own secret; the same one keeps the stored one.
	w = ctxcapMobile(t, router, http.MethodPut, path+"slack/oauth-app", testUserID, sceneBody(map[string]any{"client_id": "ctxcap-scene-client-2"}))
	if w.Code != http.StatusBadRequest || catalogErrorCode(t, w) != "oauth_app_field_required" {
		t.Fatalf("new client without its secret: %d %s", w.Code, w.Body.String())
	}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, path+"slack/oauth-app", testUserID,
		sceneBody(map[string]any{"client_id": "ctxcap-scene-client"})), http.StatusOK, "manager keeps the secret")
	w = ctxcapMobile(t, router, http.MethodPut, path+"slack/oauth-app", testUserID,
		sceneBody(map[string]any{"client_id": "ctxcap-scene-client-2", "client_secret": "ctxcap-scene-secret-2"}))
	ctxcapExpectStatus(t, w, http.StatusOK, "manager change")
	ctxcapDecode(t, w, &view)
	if view.ClientID != "ctxcap-scene-client-2" || !view.ClientSecretSet || !view.CanEdit {
		t.Fatalf("changed view = %+v", view)
	}

	// Another scene of the agent has none, and its members cannot reach this one.
	w = ctxcapMobile(t, router, http.MethodGet, path+"slack/oauth-app?scope_type=scene&scope_key="+ctxcapOtherScene, testUserID, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "manager reads another scene")
	var other contextConfigOAuthAppView
	ctxcapDecode(t, w, &other)
	if other.Saved || other.ClientID != "" {
		t.Fatalf("another scene sees this one's application: %+v", other)
	}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodGet, path+"slack/oauth-app?scope_type=scene&scope_key="+ctxcapOtherScene, holder, nil),
		http.StatusForbidden, "holder reads another scene")

	// The scene detail names the apps it signs in to with its own application.
	var scene struct {
		SceneOAuthApps []string `json:"scene_oauth_apps"`
	}
	ctxcapDecode(t, ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID+"/scenes/"+ctxcapScene, holder, nil), &scene)
	if len(scene.SceneOAuthApps) != 1 || scene.SceneOAuthApps[0] != "slack" {
		t.Fatalf("scene_oauth_apps = %v", scene.SceneOAuthApps)
	}

	// A connection started at the scene authorizes with the scene's client.
	before := ctxcapCatalogConnectors(t, "slack")
	t.Cleanup(func() {
		for id := range ctxcapCatalogConnectors(t, "slack") {
			if !before[id] {
				_, _ = testPool.Exec(context.Background(), `DELETE FROM internal_connector WHERE id = $1`, id)
			}
		}
		_, _ = testPool.Exec(context.Background(), `DELETE FROM connector_oauth_state WHERE agent_id = $1`, agentID)
	})
	w = ctxcapMobile(t, router, http.MethodPost, path+"slack", holder, sceneBody(nil))
	ctxcapExpectStatus(t, w, http.StatusOK, "add slack")
	var added struct {
		ConnectorID string `json:"connector_id"`
	}
	ctxcapDecode(t, w, &added)
	w = ctxcapMobile(t, router, http.MethodPost, "/api/context-capabilities/agents/"+agentID+"/connections/start", holder,
		sceneBody(map[string]any{"connector_id": added.ConnectorID}))
	ctxcapExpectStatus(t, w, http.StatusOK, "start at the scene")
	var started struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	ctxcapDecode(t, w, &started)
	authorize, err := url.Parse(started.AuthorizeURL)
	if err != nil || authorize.Query().Get("client_id") != "ctxcap-scene-client-2" {
		t.Fatalf("authorize_url = %q", started.AuthorizeURL)
	}

	// Removing it, back to the workspace's: the agent's managers only.
	w = ctxcapMobile(t, router, http.MethodDelete, path+"slack/oauth-app"+sceneQuery, holder, nil)
	if w.Code != http.StatusForbidden || catalogErrorCode(t, w) != contextConfigErrOAuthAppLocked {
		t.Fatalf("holder remove: %d %s", w.Code, w.Body.String())
	}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodDelete, path+"slack/oauth-app"+sceneQuery, testUserID, nil), http.StatusNoContent, "manager remove")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodDelete, path+"slack/oauth-app"+sceneQuery, testUserID, nil), http.StatusNoContent, "remove again")
	ctxcapDecode(t, ctxcapMobile(t, router, http.MethodGet, path+"slack/oauth-app"+sceneQuery, holder, nil), &view)
	if view.Saved || !view.CanEdit {
		t.Fatalf("after removal = %+v", view)
	}
}

// A scene's own OAuth application exchanges and refreshes the tokens it
// issued; tokens of another client stay with the workspace's, and other
// scopes never see it.
func TestSceneOAuthApplicationTokenEndpoint(t *testing.T) {
	f := newCtxcapFixture(t)
	f.h.cfg.AppURL = "https://app.multica.example"
	agentID := uuidToString(f.agent)
	if len(ctxcapConnectorApps(t, "slack")) > 0 {
		t.Skip("the test workspace already has a Slack OAuth application")
	}
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM context_connector_app WHERE agent_id = $1`, agentID)
	})
	app, ok := catalogApp("slack")
	if !ok {
		t.Fatal("slack is not in the catalog")
	}
	binding := contextcap.CredentialBinding{WorkspaceID: testWorkspaceID, AgentID: agentID, ConnectorID: uuid.NewString(), ScopeType: contextcap.ScopeScene, OrgID: ctxcapOrg, ScopeKey: ctxcapScene}
	key, _ := contextcap.SceneAppKeyOf(binding, "slack")
	sealed, hint, err := f.h.sealConnectorAppSecret("scene-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := contextcap.CreateSceneApp(ctx, testPool, key, "scene-client", sealed, hint, testUserID); err != nil {
		t.Fatal(err)
	}
	if err := contextcap.CreateSceneApp(ctx, testPool, key, "again", sealed, hint, testUserID); !errors.Is(err, contextcap.ErrSceneAppExists) {
		t.Fatalf("second create = %v", err)
	}

	client, own, err := f.h.sceneOAuthClient(ctx, binding, app)
	if err != nil || !own || client.ClientID != "scene-client" || client.ClientSecret != "scene-secret" || !client.Scene {
		t.Fatalf("scene client = %+v %v %v", client, own, err)
	}
	person := binding
	person.ScopeType, person.ScopeKey = contextcap.ScopePerson, "staff-1"
	if _, own, err := f.h.sceneOAuthClient(ctx, person, app); own || err != nil {
		t.Fatalf("person scope sees the scene application: %v %v", own, err)
	}
	if err := f.h.connectorOAuthDeploymentErrorForScope(ctx, binding, app); err != nil {
		t.Fatalf("scene sign-in not ready: %v", err)
	}
	if err := f.h.connectorOAuthDeploymentErrorForScope(ctx, person, app); err == nil {
		t.Fatal("a person scope signs in without the workspace's application")
	}

	c := internalConnector{CatalogSlug: "slack", WorkspaceID: testWorkspaceID, credentialKey: binding}
	endpoint, err := f.h.connectorTokenEndpoint(ctx, c, "scene-client")
	if err != nil || !endpoint.scene || endpoint.registration.ClientID != "scene-client" || endpoint.registration.ClientSecret != "scene-secret" ||
		!strings.HasSuffix(endpoint.redirectURI, connectorOAuthCallbackPath) {
		t.Fatalf("scene token endpoint = %+v %v", endpoint, err)
	}
	// A token another client issued (before the scene saved its own) stays
	// with the workspace's client, which this workspace lacks.
	if _, err := f.h.connectorTokenEndpoint(ctx, c, "workspace-client"); err == nil {
		t.Fatal("a token of another client used the scene's application")
	}

	// A scene secret that cannot be opened fails only the tokens it issued.
	if _, err := testPool.Exec(ctx, `UPDATE context_connector_app SET client_secret_ciphertext = '\x00' WHERE agent_id = $1`, agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.connectorTokenEndpoint(ctx, c, "scene-client"); !errors.Is(err, connectorconfig.ErrSecretUnavailable) {
		t.Fatalf("scene token with a broken secret: %v", err)
	}
	if _, err := f.h.connectorTokenEndpoint(ctx, c, "workspace-client"); err == nil || errors.Is(err, connectorconfig.ErrSecretUnavailable) {
		t.Fatalf("workspace token blocked by the scene's broken secret: %v", err)
	}
}
