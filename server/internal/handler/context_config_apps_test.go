package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

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

// A level adds an official app nobody opened for the agent: the workspace
// installs it, the agent offers it, and the level switches it on.
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

	w := ctxcapMobile(t, router, http.MethodPost, path+"notion", holder, body)
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
	// Adding again is a no-op on the same connector.
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
}

// An app without dynamic registration takes its OAuth application on the
// configure page, from the agent's managers only, without returning secrets.
func TestContextConfigOAuthApplicationForManagers(t *testing.T) {
	f := newCtxcapFixture(t)
	// A sign-in needs the app origin it returns to.
	f.h.cfg.AppURL = "https://app.multica.example"
	router := ctxcapAppsRouter(f.h)
	agentID := uuidToString(f.agent)
	holder := uuid.NewString()
	f.grant(t, holder, contextcap.ScopeScene, ctxcapScene, "Ctxcap group")
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM connector_app WHERE workspace_id = $1 AND provider = 'slack' AND client_id = 'ctxcap-slack-client'`, testWorkspaceID)
	})
	path := "/api/context-capabilities/agents/" + agentID + "/apps/"

	w := ctxcapMobile(t, router, http.MethodGet, path+"slack/oauth-app", holder, nil)
	if w.Code != http.StatusForbidden || catalogErrorCode(t, w) != contextCapErrManagerOnly {
		t.Fatalf("holder read: %d %s", w.Code, w.Body.String())
	}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, path+"slack/oauth-app", holder,
		map[string]any{"client_id": "x", "client_secret": "y"}), http.StatusForbidden, "holder write")
	// A dynamic-registration app needs none.
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodGet, path+"notion/oauth-app", testUserID, nil), http.StatusNotFound, "notion")

	w = ctxcapMobile(t, router, http.MethodGet, path+"slack/oauth-app", testUserID, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "manager read")
	var view contextConfigOAuthAppView
	ctxcapDecode(t, w, &view)
	if view.CallbackURL == "" || len(view.Fields) == 0 {
		t.Fatalf("view = %+v", view)
	}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, path+"slack/oauth-app", testUserID,
		map[string]any{"client_id": "ctxcap-slack-client"}), http.StatusBadRequest, "no secret")

	w = ctxcapMobile(t, router, http.MethodPut, path+"slack/oauth-app", testUserID,
		map[string]any{"client_id": "ctxcap-slack-client", "client_secret": "ctxcap-slack-secret"})
	ctxcapExpectStatus(t, w, http.StatusOK, "manager save")
	if strings.Contains(w.Body.String(), "ctxcap-slack-secret") {
		t.Fatalf("the secret came back: %s", w.Body.String())
	}
	ctxcapDecode(t, w, &view)
	if !view.Ready || view.ClientID != "ctxcap-slack-client" || !view.ClientSecretSet {
		t.Fatalf("saved view = %+v", view)
	}
	// A later save without the secret keeps it.
	w = ctxcapMobile(t, router, http.MethodPut, path+"slack/oauth-app", testUserID, map[string]any{"client_id": "ctxcap-slack-client"})
	ctxcapExpectStatus(t, w, http.StatusOK, "save keeping the secret")

	// The detail says Slack is ready, and who may configure apps.
	type detailApps struct {
		Apps             []contextCapCatalogAppDTO `json:"apps"`
		CanConfigureApps bool                      `json:"can_configure_apps"`
	}
	var managerDetail, holderDetail detailApps
	ctxcapDecode(t, ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID, testUserID, nil), &managerDetail)
	ctxcapDecode(t, ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID, holder, nil), &holderDetail)
	if !managerDetail.CanConfigureApps || holderDetail.CanConfigureApps {
		t.Fatalf("can configure: manager %v holder %v", managerDetail.CanConfigureApps, holderDetail.CanConfigureApps)
	}
	setup := map[string]contextCapCatalogAppDTO{}
	for _, app := range managerDetail.Apps {
		setup[app.Slug] = app
	}
	if setup["slack"].Setup != "oauth_app" || !setup["slack"].Ready || setup["notion"].Setup != "automatic" {
		t.Fatalf("apps = %+v", managerDetail.Apps)
	}
}
