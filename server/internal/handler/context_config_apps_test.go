package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/connectorcatalog"
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

// An app without dynamic registration takes its OAuth application on the
// configure page: anyone who may open the agent saves the first one; the
// saved one is the workspace's client, which only workspace admins change
// (an agent owner who is a plain member included). Secrets never come back,
// and an admin's callback mode stays.
func TestContextConfigOAuthApplicationOnThePage(t *testing.T) {
	f := newCtxcapFixture(t)
	// A sign-in needs the app origin it returns to.
	f.h.cfg.AppURL = "https://app.multica.example"
	router := ctxcapAppsRouter(f.h)
	agentID := uuidToString(f.agent)
	holder, stranger := uuid.NewString(), uuid.NewString()
	f.grant(t, holder, contextcap.ScopeScene, ctxcapScene, "Ctxcap group")
	before := ctxcapConnectorApps(t, "slack")
	if len(before) > 0 {
		t.Skip("the test workspace already has a Slack OAuth application")
	}
	t.Cleanup(func() {
		for id := range ctxcapConnectorApps(t, "slack") {
			_, _ = testPool.Exec(context.Background(), `DELETE FROM connector_app WHERE id = $1`, id)
		}
	})
	path := "/api/context-capabilities/agents/" + agentID + "/apps/"
	ctx := context.Background()

	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodGet, path+"slack/oauth-app", stranger, nil), http.StatusForbidden, "stranger read")
	// A dynamic-registration app needs none.
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodGet, path+"notion/oauth-app", holder, nil), http.StatusNotFound, "notion")

	w := ctxcapMobile(t, router, http.MethodGet, path+"slack/oauth-app", holder, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "holder read")
	var view contextConfigOAuthAppView
	ctxcapDecode(t, w, &view)
	if view.CallbackURL != connectorcatalog.ProductionCallbackURL || len(view.Fields) == 0 || view.Saved || view.Ready {
		t.Fatalf("view = %+v", view)
	}
	w = ctxcapMobile(t, router, http.MethodPut, path+"slack/oauth-app", holder, map[string]any{"client_id": "ctxcap-slack-client"})
	if w.Code != http.StatusBadRequest || catalogErrorCode(t, w) != "oauth_app_field_required" {
		t.Fatalf("save without the secret: %d %s", w.Code, w.Body.String())
	}

	// The first one: a scene's link holder fills it in.
	w = ctxcapMobile(t, router, http.MethodPut, path+"slack/oauth-app", holder,
		map[string]any{"client_id": "ctxcap-slack-client", "client_secret": "ctxcap-slack-secret"})
	ctxcapExpectStatus(t, w, http.StatusOK, "holder saves the first one")
	if strings.Contains(w.Body.String(), "ctxcap-slack-secret") {
		t.Fatalf("the secret came back: %s", w.Body.String())
	}
	ctxcapDecode(t, w, &view)
	if !view.Ready || !view.Saved || view.ClientID != "ctxcap-slack-client" || !view.ClientSecretSet {
		t.Fatalf("saved view = %+v", view)
	}

	// Changing it: workspace admins only, not the holder nor an agent owner
	// who is a plain member.
	w = ctxcapMobile(t, router, http.MethodPut, path+"slack/oauth-app", holder, map[string]any{"client_id": "other", "client_secret": "other"})
	if w.Code != http.StatusForbidden || catalogErrorCode(t, w) != contextConfigErrAppRequiresAdmin {
		t.Fatalf("holder change: %d %s", w.Code, w.Body.String())
	}
	member := createWorkspaceMemberUser(t, "Ctxcap owner", "ctxcap-owner-"+uuid.NewString()[:8]+"@example.test")
	if _, err := testPool.Exec(ctx, `UPDATE agent SET owner_id = $1 WHERE id = $2`, member, agentID); err != nil {
		t.Fatal(err)
	}
	w = ctxcapMobile(t, router, http.MethodPut, path+"slack/oauth-app", member, map[string]any{"client_id": "other", "client_secret": "other"})
	if w.Code != http.StatusForbidden || catalogErrorCode(t, w) != contextConfigErrAppRequiresAdmin {
		t.Fatalf("plain-member agent owner change: %d %s", w.Code, w.Body.String())
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent SET owner_id = $1 WHERE id = $2`, testUserID, agentID); err != nil {
		t.Fatal(err)
	}

	// An admin changes it; an omitted secret and the callback mode stay.
	if _, err := testPool.Exec(ctx, `UPDATE connector_app SET callback_mode = 'self' WHERE workspace_id = $1 AND provider = 'slack'`, testWorkspaceID); err != nil {
		t.Fatal(err)
	}
	w = ctxcapMobile(t, router, http.MethodPut, path+"slack/oauth-app", testUserID, map[string]any{"client_id": "ctxcap-slack-client-2"})
	ctxcapExpectStatus(t, w, http.StatusOK, "admin change")
	ctxcapDecode(t, w, &view)
	if view.ClientID != "ctxcap-slack-client-2" || !view.ClientSecretSet || view.CallbackURL != "https://app.multica.example"+connectorOAuthCallbackPath {
		t.Fatalf("changed view = %+v", view)
	}
	var mode string
	if err := testPool.QueryRow(ctx, `SELECT callback_mode FROM connector_app WHERE workspace_id = $1 AND provider = 'slack'`, testWorkspaceID).Scan(&mode); err != nil || mode != "self" {
		t.Fatalf("callback mode = %q %v", mode, err)
	}

	// The detail says Slack is ready, and who may change saved applications.
	type detailApps struct {
		Apps             []contextCapCatalogAppDTO `json:"apps"`
		CanConfigureApps bool                      `json:"can_configure_apps"`
	}
	var adminDetail, holderDetail detailApps
	ctxcapDecode(t, ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID, testUserID, nil), &adminDetail)
	ctxcapDecode(t, ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID, holder, nil), &holderDetail)
	if !adminDetail.CanConfigureApps || holderDetail.CanConfigureApps {
		t.Fatalf("can configure: admin %v holder %v", adminDetail.CanConfigureApps, holderDetail.CanConfigureApps)
	}
	setup := map[string]contextCapCatalogAppDTO{}
	for _, app := range adminDetail.Apps {
		setup[app.Slug] = app
	}
	if setup["slack"].Setup != "oauth_app" || !setup["slack"].Ready || setup["notion"].Setup != "automatic" {
		t.Fatalf("apps = %+v", adminDetail.Apps)
	}
}
