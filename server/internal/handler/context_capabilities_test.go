package handler

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/integrations/dingtalk"
)

// ctxcapRouter mirrors the context capability wiring in cmd/server/router.go.
func ctxcapRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Route("/api/context-capabilities", func(r chi.Router) {
		r.Use(RequireDingTalkHumanActor)
		r.Post("/links/redeem", h.RedeemContextConfigLink)
		r.Get("/agents", h.ListContextConfigAgents)
		r.Get("/agents/{agentId}", h.GetContextConfigAgent)
		r.Get("/agents/{agentId}/scenes/{sceneKey}", h.GetContextConfigScene)
		r.Post("/agents/{agentId}/scenes/resolve", h.ResolveContextConfigScene)
		r.Put("/agents/{agentId}/bindings", h.PutContextConfigBinding)
		r.Put("/agents/{agentId}/credentials", h.PutContextConfigCredential)
		r.Delete("/agents/{agentId}/credentials", h.DeleteContextConfigCredential)
	})
	r.With(RequireHumanActor).Get("/api/dingtalk/jsapi-config", h.GetDingTalkJSAPIConfig)
	r.Route("/api/agents/{id}", func(r chi.Router) {
		r.With(RequireHumanActor).Get("/context-capabilities", h.GetAgentContextCapabilities)
		r.With(RequireHumanActor).Put("/context-capabilities/offers", h.PutAgentContextCapabilityOffers)
	})
	return r
}

// ctxcapMobile issues a request as a DingTalk-authenticated human session.
func ctxcapMobile(t *testing.T, h http.Handler, method, path, userID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	switch value := body.(type) {
	case nil:
		reader = bytes.NewReader(nil)
	case string:
		reader = bytes.NewReader([]byte(value))
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", userID)
	req.Header.Set("X-Auth-Method", "dingtalk")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func ctxcapDecode(t *testing.T, w *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), target); err != nil {
		t.Fatalf("decode %d body %q: %v", w.Code, w.Body.String(), err)
	}
}

func ctxcapExpectStatus(t *testing.T, w *httptest.ResponseRecorder, want int, what string) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("%s: status=%d want %d body=%s", what, w.Code, want, w.Body.String())
	}
}

// grant plants a live grant for userID and removes grants/links of the
// fixture agent afterwards.
func (f *ctxcapFixture) grant(t *testing.T, userID, scopeType, key, title string) {
	t.Helper()
	f.cleanupGrantsAndLinks(t)
	if _, err := contextcap.UpsertGrant(context.Background(), testPool, contextcap.Grant{
		UserID: userID, WorkspaceID: testWorkspaceID, AgentID: uuidToString(f.agent), ScopeType: scopeType,
		OrgID: ctxcapOrg, ScopeKey: key, ScopeTitle: title, Source: contextcap.GrantSourceAgentLink,
	}, contextcap.GrantTTL(scopeType)); err != nil {
		t.Fatal(err)
	}
}

func (f *ctxcapFixture) cleanupGrantsAndLinks(t *testing.T) {
	t.Helper()
	agentID := uuidToString(f.agent)
	t.Cleanup(func() {
		for _, statement := range []string{
			`DELETE FROM context_config_grant WHERE agent_id = $1`,
			`DELETE FROM context_config_link WHERE agent_id = $1`,
			`DELETE FROM scene_memory WHERE agent_id = $1`,
		} {
			_, _ = testPool.Exec(context.Background(), statement, agentID)
		}
	})
}

// ctxcapScenePath encodes the scene key like the web client's
// encodeURIComponent, which escapes the "=", "+" and "/" of an
// openConversationId.
func ctxcapScenePath(agentID, sceneKey string) string {
	return "/api/context-capabilities/agents/" + agentID + "/scenes/" + strings.ReplaceAll(url.QueryEscape(sceneKey), "+", "%20")
}

func TestContextCapabilitiesScenePathKeepsEscapedOpenConversationID(t *testing.T) {
	f := newCtxcapFixture(t)
	router := ctxcapRouter(f.h)
	agentID := uuidToString(f.agent)
	alice := uuid.NewString()
	const slashyScene = "cidAb+Cd/Ef=="
	f.grant(t, alice, contextcap.ScopeScene, slashyScene, "Slashy group")
	path := ctxcapScenePath(agentID, slashyScene)
	if !strings.Contains(path, "%2B") || !strings.Contains(path, "%2F") || !strings.Contains(path, "%3D") {
		t.Fatalf("path %q does not exercise escaping", path)
	}
	w := ctxcapMobile(t, router, http.MethodGet, path, alice, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "escaped scene key")
	var scene ctxcapSceneDetail
	ctxcapDecode(t, w, &scene)
	if scene.Scene.ScopeKey != slashyScene || scene.Scene.ScopeTitle != "Slashy group" {
		t.Fatalf("scene=%+v", scene.Scene)
	}
	// Unescaped padding also works (no RawPath).
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID+"/scenes/"+ctxcapScene, alice, nil),
		http.StatusForbidden, "raw padding, no grant")
}

type ctxcapAgentDetail struct {
	Agent struct {
		ID          string  `json:"id"`
		Name        string  `json:"name"`
		AvatarURL   *string `json:"avatar_url"`
		WorkspaceID string  `json:"workspace_id"`
	} `json:"agent"`
	Global struct {
		Connectors []struct{ ID, Name string } `json:"connectors"`
		Skills     []struct{ ID, Name string } `json:"skills"`
	} `json:"global"`
	Offers struct {
		Connectors []struct {
			ID                 string   `json:"id"`
			Name               string   `json:"name"`
			Tools              []string `json:"tools"`
			AcceptsCredential  bool     `json:"accepts_credential"`
			CredentialRequired bool     `json:"credential_required"`
		} `json:"connectors"`
		Skills []struct{ ID, Name, Description string } `json:"skills"`
	} `json:"offers"`
	Person *struct {
		ScopeKey    string                    `json:"scope_key"`
		Source      string                    `json:"source"`
		ExpiresAt   string                    `json:"expires_at"`
		Bindings    []contextCapBindingDTO    `json:"bindings"`
		Credentials []contextCapCredentialDTO `json:"credentials"`
	} `json:"person"`
	Scenes         []contextCapSceneDTO `json:"scenes"`
	JSAPIAvailable bool                 `json:"jsapi_available"`
}

type ctxcapSceneDetail struct {
	Scene       contextCapSceneDTO        `json:"scene"`
	Scope       *contextCapScopeRef       `json:"scope"`
	Bindings    []contextCapBindingDTO    `json:"bindings"`
	Credentials []contextCapCredentialDTO `json:"credentials"`
	CanConnect  bool                      `json:"can_connect"`
}

func ctxcapHasBinding(bindings []contextCapBindingDTO, resourceID string, enabled bool) bool {
	for _, binding := range bindings {
		if binding.ResourceID == resourceID && binding.Enabled == enabled {
			return true
		}
	}
	return false
}

func ctxcapMentions(bindings []contextCapBindingDTO, resourceID string) bool {
	for _, binding := range bindings {
		if binding.ResourceID == resourceID {
			return true
		}
	}
	return false
}

func TestContextCapabilitiesMobileRequiresDingTalkSessionAndFlag(t *testing.T) {
	f := newCtxcapFixture(t)
	router := ctxcapRouter(f.h)
	user := uuid.NewString()

	req := httptest.NewRequest(http.MethodGet, "/api/context-capabilities/agents", nil)
	req.Header.Set("X-User-ID", user)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	ctxcapExpectStatus(t, w, http.StatusForbidden, "non-DingTalk session")

	req = httptest.NewRequest(http.MethodGet, "/api/context-capabilities/agents", nil)
	req.Header.Set("X-User-ID", user)
	req.Header.Set("X-Auth-Method", "dingtalk")
	req.Header.Set("X-Actor-Source", "task_token")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	ctxcapExpectStatus(t, w, http.StatusForbidden, "task token")

	w = ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents", user, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "dingtalk session")
}

func TestContextCapabilitiesMobileGrantsGateReadsAndWrites(t *testing.T) {
	f := newCtxcapFixture(t)
	router := ctxcapRouter(f.h)
	agentID := uuidToString(f.agent)
	alice, bob := uuid.NewString(), uuid.NewString()
	f.grant(t, alice, contextcap.ScopeScene, ctxcapScene, "Ctxcap group")
	f.grant(t, alice, contextcap.ScopePerson, ctxcapStaff, "Alice")

	// Agent list: only the caller's own grants.
	w := ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents", alice, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "alice agents")
	var list struct {
		Agents []struct {
			ID          string               `json:"id"`
			WorkspaceID string               `json:"workspace_id"`
			Scopes      []contextCapGrantDTO `json:"scopes"`
		} `json:"agents"`
	}
	ctxcapDecode(t, w, &list)
	if len(list.Agents) != 1 || list.Agents[0].ID != agentID || list.Agents[0].WorkspaceID != testWorkspaceID || len(list.Agents[0].Scopes) != 2 {
		t.Fatalf("alice agents=%+v", list.Agents)
	}
	w = ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents", bob, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "bob agents")
	if !strings.Contains(w.Body.String(), `"agents":[]`) {
		t.Fatalf("bob sees agents: %s", w.Body.String())
	}

	// Agent detail.
	w = ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID, bob, nil)
	ctxcapExpectStatus(t, w, http.StatusForbidden, "bob detail")
	w = ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+uuid.NewString(), alice, nil)
	ctxcapExpectStatus(t, w, http.StatusNotFound, "unknown agent")
	w = ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID, alice, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "alice detail")
	for _, forbidden := range []string{"upstream_url", "credential_ref", "safe.example.test", "workspace-secret", "credential_source"} {
		if strings.Contains(w.Body.String(), forbidden) {
			t.Fatalf("agent detail leaks %q: %s", forbidden, w.Body.String())
		}
	}
	var detail ctxcapAgentDetail
	ctxcapDecode(t, w, &detail)
	if detail.Agent.ID != agentID || len(detail.Global.Connectors) != 1 || detail.Global.Connectors[0].ID != f.global ||
		len(detail.Global.Skills) != 1 || detail.Global.Skills[0].ID != f.skillAgent {
		t.Fatalf("global=%+v agent=%+v", detail.Global, detail.Agent)
	}
	offered := map[string]bool{}
	for _, c := range detail.Offers.Connectors {
		offered[c.ID] = true
		switch c.ID {
		case f.scene:
			if !c.AcceptsCredential || !c.CredentialRequired || len(c.Tools) != 1 || c.Tools[0] != "read" {
				t.Fatalf("scene connector offer=%+v", c)
			}
		case f.person:
			if c.AcceptsCredential || c.CredentialRequired {
				t.Fatalf("auth none connector offer=%+v", c)
			}
		}
	}
	if len(offered) != 2 || !offered[f.scene] || !offered[f.person] || len(detail.Offers.Skills) != 1 || detail.Offers.Skills[0].ID != f.skillScene {
		t.Fatalf("offers=%+v", detail.Offers)
	}
	if detail.Person == nil || detail.Person.ScopeKey != ctxcapStaff || detail.Person.Source != contextcap.GrantSourceAgentLink ||
		!ctxcapHasBinding(detail.Person.Bindings, f.person, true) || detail.Person.ExpiresAt == "" {
		t.Fatalf("person=%+v", detail.Person)
	}
	if len(detail.Scenes) != 1 || detail.Scenes[0].ScopeKey != ctxcapScene || detail.Scenes[0].ScopeTitle != "Ctxcap group" {
		t.Fatalf("scenes=%+v", detail.Scenes)
	}
	if detail.JSAPIAvailable {
		t.Fatal("jsapi_available without H5 configuration")
	}

	// Scene detail: the escaped openConversationId round-trips, bindings of
	// resources outside the offer catalog are hidden.
	w = ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, ctxcapScene), alice, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "alice scene")
	var scene ctxcapSceneDetail
	ctxcapDecode(t, w, &scene)
	if scene.Scene.ScopeKey != ctxcapScene || !ctxcapHasBinding(scene.Bindings, f.scene, true) || !ctxcapHasBinding(scene.Bindings, f.skillScene, true) ||
		ctxcapMentions(scene.Bindings, f.notOffered) || ctxcapMentions(scene.Bindings, f.skillFree) {
		t.Fatalf("scene detail=%+v", scene)
	}
	w = ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, ctxcapScene), bob, nil)
	ctxcapExpectStatus(t, w, http.StatusForbidden, "bob scene")
	w = ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, ctxcapOtherScene), alice, nil)
	ctxcapExpectStatus(t, w, http.StatusForbidden, "alice other scene")

	// Binding writes.
	bindingPath := "/api/context-capabilities/agents/" + agentID + "/bindings"
	write := func(user, scopeType, key, resourceType, resourceID string, enabled bool) *httptest.ResponseRecorder {
		return ctxcapMobile(t, router, http.MethodPut, bindingPath, user, map[string]any{
			"scope_type": scopeType, "scope_key": key, "resource_type": resourceType, "resource_id": resourceID, "enabled": enabled,
		})
	}
	w = write(alice, contextcap.ScopePerson, ctxcapStaff, contextcap.ResourceSkill, f.skillScene, true)
	ctxcapExpectStatus(t, w, http.StatusOK, "enable person skill")
	var bindingResp struct {
		Binding contextCapBindingDTO `json:"binding"`
	}
	ctxcapDecode(t, w, &bindingResp)
	if bindingResp.Binding != (contextCapBindingDTO{ResourceType: contextcap.ResourceSkill, ResourceID: f.skillScene, Enabled: true}) {
		t.Fatalf("binding=%+v", bindingResp.Binding)
	}
	w = write(alice, contextcap.ScopePerson, ctxcapStaff, contextcap.ResourceSkill, f.skillScene, false)
	ctxcapExpectStatus(t, w, http.StatusOK, "disable person skill")
	ctxcapDecode(t, w, &bindingResp)
	if bindingResp.Binding.Enabled {
		t.Fatal("binding still enabled")
	}
	ctxcapExpectStatus(t, write(alice, contextcap.ScopeScene, ctxcapScene, contextcap.ResourceConnector, f.notOffered, true), http.StatusForbidden, "non-offered connector")
	ctxcapExpectStatus(t, write(alice, contextcap.ScopeScene, ctxcapScene, contextcap.ResourceSkill, f.skillFree, false), http.StatusForbidden, "non-offered skill disable")
	ctxcapExpectStatus(t, write(alice, contextcap.ScopeScene, ctxcapScene, contextcap.ResourceSkill, f.skillAgent, true), http.StatusForbidden, "global-only skill")
	ctxcapExpectStatus(t, write(bob, contextcap.ScopeScene, ctxcapScene, contextcap.ResourceSkill, f.skillScene, true), http.StatusForbidden, "bob write")
	ctxcapExpectStatus(t, write(alice, contextcap.ScopeScene, ctxcapOtherScene, contextcap.ResourceSkill, f.skillScene, true), http.StatusForbidden, "ungranted scene write")
	ctxcapExpectStatus(t, write(alice, contextcap.ScopePerson, ctxcapOtherStaff, contextcap.ResourceSkill, f.skillScene, true), http.StatusForbidden, "other person write")
	ctxcapExpectStatus(t, write(alice, "offer", "", contextcap.ResourceSkill, f.skillScene, true), http.StatusBadRequest, "offer scope write")
	ctxcapExpectStatus(t, write(alice, contextcap.ScopeScene, ctxcapScene, "prompt", f.skillScene, true), http.StatusBadRequest, "bad resource type")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, bindingPath, alice, map[string]any{
		"scope_type": contextcap.ScopeScene, "scope_key": ctxcapScene, "resource_type": contextcap.ResourceSkill, "resource_id": f.skillScene,
	}), http.StatusBadRequest, "missing enabled")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, bindingPath, alice, map[string]any{
		"scope_type": contextcap.ScopeScene, "scope_key": ctxcapScene, "resource_type": contextcap.ResourceSkill, "resource_id": f.skillScene,
		"enabled": true, "org_id": "other",
	}), http.StatusBadRequest, "unknown field")
}

func TestContextCapabilitiesMobileCredentialsAreWriteOnly(t *testing.T) {
	f := newCtxcapFixture(t)
	router := ctxcapRouter(f.h)
	agentID := uuidToString(f.agent)
	alice, bob := uuid.NewString(), uuid.NewString()
	f.grant(t, alice, contextcap.ScopeScene, ctxcapScene, "Ctxcap group")
	f.grant(t, alice, contextcap.ScopePerson, ctxcapStaff, "Alice")
	credentialPath := "/api/context-capabilities/agents/" + agentID + "/credentials"
	put := func(user, scopeType, key, connectorID, bearer string) *httptest.ResponseRecorder {
		return ctxcapMobile(t, router, http.MethodPut, credentialPath, user, map[string]any{
			"scope_type": scopeType, "scope_key": key, "connector_id": connectorID, "bearer": bearer,
		})
	}

	const personSecret = "person-secret-value-7788"
	w := put(alice, contextcap.ScopePerson, ctxcapStaff, f.scene, personSecret)
	ctxcapExpectStatus(t, w, http.StatusOK, "person credential")
	if strings.Contains(w.Body.String(), personSecret) || strings.Contains(w.Body.String(), "secret-value") {
		t.Fatalf("credential response echoes the secret: %s", w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control=%q", w.Header().Get("Cache-Control"))
	}
	var credResp struct {
		Credential contextCapCredentialDTO `json:"credential"`
	}
	ctxcapDecode(t, w, &credResp)
	if credResp.Credential.ConnectorID != f.scene || credResp.Credential.Hint != "••••7788" || credResp.Credential.UpdatedAt == "" {
		t.Fatalf("credential=%+v", credResp.Credential)
	}
	var ciphertext []byte
	if err := testPool.QueryRow(context.Background(), `SELECT ciphertext FROM context_connector_credential
		WHERE agent_id = $1 AND connector_id = $2 AND scope_type = 'person' AND scope_key = $3`, agentID, f.scene, ctxcapStaff).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(personSecret)) {
		t.Fatal("credential stored in plaintext")
	}

	// The API-written personal credential wins over the scene layer for the
	// scene-bound connector when Alice triggers a run in the group.
	resolved := f.resolve(t, f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff)))
	if got := resolved[f.scene]; got.binding != connectorBindingScene || got.credential != connectorCredentialPerson || got.bearer != personSecret {
		t.Fatalf("scene connector resolution=%+v", got)
	}
	// Someone else in the same group does not get Alice's credential; the
	// scene connector has no other credential, so it is not mounted.
	if _, ok := f.resolve(t, f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapOtherStaff, ctxcapOtherStaff)))[f.scene]; ok {
		t.Fatal("scene connector mounted with another person's credential")
	}

	// A globally granted Bearer connector that is not offered accepts a
	// personal token (it only serves Alice's own runs) but no scene token,
	// which would serve every member's runs without the admin's opt-in.
	ctxcapExpectStatus(t, put(alice, contextcap.ScopePerson, ctxcapStaff, f.global, "person-secret-for-global"), http.StatusOK, "global connector person credential")
	ctxcapExpectStatus(t, put(alice, contextcap.ScopeScene, ctxcapScene, f.global, "scene-secret-for-global"), http.StatusForbidden, "global connector scene credential")
	ctxcapExpectStatus(t, put(alice, contextcap.ScopeScene, ctxcapScene, f.scene, "scene-secret-for-scene"), http.StatusOK, "offered connector scene credential")
	ctxcapExpectStatus(t, put(alice, contextcap.ScopeScene, ctxcapScene, f.person, "whatever-bearer"), http.StatusBadRequest, "auth none connector")
	ctxcapExpectStatus(t, put(alice, contextcap.ScopeScene, ctxcapScene, f.notOffered, "whatever-bearer"), http.StatusForbidden, "not offered connector")
	ctxcapExpectStatus(t, put(alice, contextcap.ScopeScene, ctxcapScene, uuid.NewString(), "whatever-bearer"), http.StatusForbidden, "unknown connector")
	ctxcapExpectStatus(t, put(alice, contextcap.ScopeScene, ctxcapScene, f.scene, "bad\nbearer"), http.StatusBadRequest, "header-unsafe bearer")
	ctxcapExpectStatus(t, put(alice, contextcap.ScopeScene, ctxcapScene, f.scene, ""), http.StatusBadRequest, "empty bearer")
	ctxcapExpectStatus(t, put(bob, contextcap.ScopeScene, ctxcapScene, f.scene, "bob-bearer-1234"), http.StatusForbidden, "bob credential")

	// Listing shows only hints.
	w = ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, ctxcapScene), alice, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "scene detail")
	if strings.Contains(w.Body.String(), "scene-secret-for-scene") {
		t.Fatalf("scene detail echoes a secret: %s", w.Body.String())
	}
	var scene ctxcapSceneDetail
	ctxcapDecode(t, w, &scene)
	if len(scene.Credentials) != 1 || scene.Credentials[0].ConnectorID != f.scene || scene.Credentials[0].Hint != "••••cene" {
		t.Fatalf("scene credentials=%+v", scene.Credentials)
	}

	// Delete is idempotent and scoped to the grant.
	deletePath := credentialPath + "?scope_type=person&scope_key=" + url.QueryEscape(ctxcapStaff) + "&connector_id=" + f.scene
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodDelete, deletePath, bob, nil), http.StatusForbidden, "bob delete")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodDelete, deletePath, alice, nil), http.StatusNoContent, "alice delete")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodDelete, deletePath, alice, nil), http.StatusNoContent, "alice delete again")
	w = ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID, alice, nil)
	var detail ctxcapAgentDetail
	ctxcapDecode(t, w, &detail)
	if detail.Person == nil || len(detail.Person.Credentials) != 1 || detail.Person.Credentials[0].ConnectorID != f.global {
		t.Fatalf("person credentials after delete=%+v", detail.Person)
	}
}

func TestContextCapabilitiesAdminOffersGateMobileBindings(t *testing.T) {
	f := newCtxcapFixture(t)
	f.h.cfg.AppURL = "https://app.multica.example/"
	router := ctxcapRouter(f.h)
	agentID := uuidToString(f.agent)
	alice := uuid.NewString()
	f.grant(t, alice, contextcap.ScopeScene, ctxcapScene, "Ctxcap group")

	admin := func(method, path string, body any) *httptest.ResponseRecorder {
		req := newRequest(method, path, body)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	type adminView struct {
		Enabled bool `json:"enabled"`
		Library struct {
			Connectors []contextCapLibraryConnectorDTO `json:"connectors"`
			Skills     []contextCapSkillDTO            `json:"skills"`
		} `json:"library"`
		Offers struct {
			ConnectorIDs []string `json:"connector_ids"`
			SkillIDs     []string `json:"skill_ids"`
		} `json:"offers"`
		Scenes       []contextCapScopeSummaryDTO `json:"scenes"`
		Persons      []contextCapScopeSummaryDTO `json:"persons"`
		ConfigureURL string                      `json:"configure_url"`
	}

	w := admin(http.MethodGet, "/api/agents/"+agentID+"/context-capabilities", nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "admin get")
	var view adminView
	ctxcapDecode(t, w, &view)
	libraryConnectors := map[string]bool{}
	for _, c := range view.Library.Connectors {
		libraryConnectors[c.ID] = true
	}
	if !view.Enabled || !libraryConnectors[f.global] || !libraryConnectors[f.notOffered] ||
		len(view.Offers.ConnectorIDs) != 2 || len(view.Offers.SkillIDs) != 1 || view.Offers.SkillIDs[0] != f.skillScene ||
		view.ConfigureURL != "https://app.multica.example/dingtalk/configure?agent="+agentID {
		t.Fatalf("admin view=%+v", view)
	}
	if len(view.Scenes) != 1 || view.Scenes[0].ScopeKey != ctxcapScene || ctxcapMentions(view.Scenes[0].Bindings, f.notOffered) ||
		!ctxcapHasBinding(view.Scenes[0].Bindings, f.scene, true) || len(view.Persons) != 1 || view.Persons[0].ScopeKey != ctxcapStaff {
		t.Fatalf("admin summaries scenes=%+v persons=%+v", view.Scenes, view.Persons)
	}
	if strings.Contains(w.Body.String(), "upstream_url") || strings.Contains(w.Body.String(), "workspace-secret") {
		t.Fatalf("admin view leaks connector internals: %s", w.Body.String())
	}

	// Non-members cannot read or write the catalog.
	outsider := newRequest(http.MethodGet, "/api/agents/"+agentID+"/context-capabilities", nil)
	outsider.Header.Set("X-User-ID", uuid.NewString())
	w = httptest.NewRecorder()
	router.ServeHTTP(w, outsider)
	if w.Code == http.StatusOK {
		t.Fatalf("outsider read the catalog: %s", w.Body.String())
	}
	taskActor := newRequest(http.MethodPut, "/api/agents/"+agentID+"/context-capabilities/offers", map[string]any{"connector_ids": []string{}, "skill_ids": []string{}})
	taskActor.Header.Set("X-Actor-Source", "task_token")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, taskActor)
	ctxcapExpectStatus(t, w, http.StatusForbidden, "task token offers write")

	// Validation.
	offersPath := "/api/agents/" + agentID + "/context-capabilities/offers"
	ctxcapExpectStatus(t, admin(http.MethodPut, offersPath, map[string]any{"connector_ids": []string{f.person}}), http.StatusBadRequest, "missing skill_ids")
	ctxcapExpectStatus(t, admin(http.MethodPut, offersPath, map[string]any{"connector_ids": []string{uuid.NewString()}, "skill_ids": []string{}}), http.StatusBadRequest, "foreign connector")
	ctxcapExpectStatus(t, admin(http.MethodPut, offersPath, map[string]any{"connector_ids": []string{"nope"}, "skill_ids": []string{}}), http.StatusBadRequest, "malformed id")

	// Removing the scene connector offer hides and blocks its bindings.
	w = admin(http.MethodPut, offersPath, map[string]any{"connector_ids": []string{f.person}, "skill_ids": []string{f.skillScene}})
	ctxcapExpectStatus(t, w, http.StatusOK, "offers replace")
	ctxcapDecode(t, w, &view)
	if len(view.Offers.ConnectorIDs) != 1 || view.Offers.ConnectorIDs[0] != f.person {
		t.Fatalf("offers after replace=%+v", view.Offers)
	}
	w = ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, ctxcapScene), alice, nil)
	var scene ctxcapSceneDetail
	ctxcapDecode(t, w, &scene)
	if ctxcapMentions(scene.Bindings, f.scene) {
		t.Fatalf("removed offer still listed: %+v", scene.Bindings)
	}
	bindingPath := "/api/context-capabilities/agents/" + agentID + "/bindings"
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, bindingPath, alice, map[string]any{
		"scope_type": contextcap.ScopeScene, "scope_key": ctxcapScene, "resource_type": contextcap.ResourceConnector, "resource_id": f.scene, "enabled": true,
	}), http.StatusForbidden, "binding for removed offer")
	if _, ok := f.resolve(t, f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff)))[f.scene]; ok {
		t.Fatal("removed offer still mounted")
	}

	// Offering it again restores the stored binding.
	ctxcapExpectStatus(t, admin(http.MethodPut, offersPath, map[string]any{"connector_ids": []string{f.person, f.scene}, "skill_ids": []string{f.skillScene}}), http.StatusOK, "re-offer")
	w = ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, ctxcapScene), alice, nil)
	ctxcapDecode(t, w, &scene)
	if !ctxcapHasBinding(scene.Bindings, f.scene, true) {
		t.Fatalf("re-offered binding missing: %+v", scene.Bindings)
	}
}

// An agent owner who is only a workspace member can manage the agent's skill
// offers but cannot add connector offers: the connector library and its
// grants are admin-only, and an offered connector may run on the workspace
// credential.
func TestContextCapabilitiesAgentOwnerCannotOfferConnectors(t *testing.T) {
	f := newCtxcapFixture(t)
	router := ctxcapRouter(f.h)
	agentID := uuidToString(f.agent)
	ctx := context.Background()
	owner := createPermissionTestMember(t, "ctxcap-owner-"+uuid.NewString()[:8]+"@example.test")
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE user_id = $1`, owner)
	})
	if _, err := testPool.Exec(ctx, `UPDATE agent SET owner_id = $1 WHERE id = $2`, owner, agentID); err != nil {
		t.Fatal(err)
	}
	type view struct {
		Library struct {
			Connectors []contextCapLibraryConnectorDTO `json:"connectors"`
		} `json:"library"`
		Offers struct {
			ConnectorIDs []string `json:"connector_ids"`
			SkillIDs     []string `json:"skill_ids"`
		} `json:"offers"`
	}
	as := func(user, method, path string, body any) *httptest.ResponseRecorder {
		req := newRequest(method, path, body)
		req.Header.Set("X-User-ID", user)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	getPath := "/api/agents/" + agentID + "/context-capabilities"
	offersPath := getPath + "/offers"

	w := as(owner, http.MethodGet, getPath, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "owner get")
	var got view
	ctxcapDecode(t, w, &got)
	listed := map[string]bool{}
	for _, c := range got.Library.Connectors {
		listed[c.ID] = true
	}
	if len(listed) != 2 || !listed[f.scene] || !listed[f.person] {
		t.Fatalf("agent owner sees connectors %v, want only the offered ones", got.Library.Connectors)
	}

	ctxcapExpectStatus(t, as(owner, http.MethodPut, offersPath, map[string]any{
		"connector_ids": []string{f.scene, f.person, f.global}, "skill_ids": []string{f.skillScene},
	}), http.StatusForbidden, "owner adds a connector offer")
	// Keeping or removing connector offers and changing skill offers is fine.
	ctxcapExpectStatus(t, as(owner, http.MethodPut, offersPath, map[string]any{
		"connector_ids": []string{strings.ToUpper(f.scene), f.person}, "skill_ids": []string{f.skillScene, f.skillAgent},
	}), http.StatusOK, "owner keeps connectors and adds a skill")
	ctxcapExpectStatus(t, as(owner, http.MethodPut, offersPath, map[string]any{
		"connector_ids": []string{f.person}, "skill_ids": []string{f.skillScene},
	}), http.StatusOK, "owner removes a connector offer")
	ctxcapExpectStatus(t, as(owner, http.MethodPut, offersPath, map[string]any{
		"connector_ids": []string{f.person, f.scene}, "skill_ids": []string{f.skillScene},
	}), http.StatusForbidden, "owner re-adds a removed connector offer")
	ctxcapExpectStatus(t, as(testUserID, http.MethodPut, offersPath, map[string]any{
		"connector_ids": []string{f.person, f.scene, f.global}, "skill_ids": []string{f.skillScene},
	}), http.StatusOK, "workspace admin offers connectors")

	// A skill another agent's source manages cannot be offered, by anyone.
	otherAgent := createHandlerTestAgent(t, "ctxcap source agent "+uuid.NewString()[:8], nil)
	var sourceID string
	if err := testPool.QueryRow(ctx, `INSERT INTO agent_source (agent_id, workspace_id, repo_owner, repo_name, ref, synced_commit_sha, created_by)
		VALUES ($1, $2, 'acme', 'ctxcap', 'main', '0123456789012345678901234567890123456789', $3) RETURNING id::text`,
		otherAgent, testWorkspaceID, testUserID).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_source_skill WHERE agent_source_id = $1`, sourceID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_source WHERE id = $1`, sourceID)
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_source_skill (agent_source_id, skill_id, source_path) VALUES ($1, $2, 'skills/free')`,
		sourceID, f.skillFree); err != nil {
		t.Fatal(err)
	}
	ctxcapExpectStatus(t, as(testUserID, http.MethodPut, offersPath, map[string]any{
		"connector_ids": []string{f.person}, "skill_ids": []string{f.skillScene, f.skillFree},
	}), http.StatusBadRequest, "another agent's source-managed skill")

	// A skill deleted without the binding sweep leaves an offer row behind; it
	// is not reported, so the tab's save round-trip keeps working.
	orphan := f.insertSkill(t, "ctxcap-orphan")
	ctxcapExpectStatus(t, as(testUserID, http.MethodPut, offersPath, map[string]any{
		"connector_ids": []string{f.person}, "skill_ids": []string{f.skillScene, orphan},
	}), http.StatusOK, "offer the soon-orphaned skill")
	if _, err := testPool.Exec(ctx, `DELETE FROM skill WHERE id = $1`, orphan); err != nil {
		t.Fatal(err)
	}
	w = as(testUserID, http.MethodGet, getPath, nil)
	ctxcapDecode(t, w, &got)
	for _, id := range got.Offers.SkillIDs {
		if id == orphan {
			t.Fatalf("deleted skill still offered: %v", got.Offers.SkillIDs)
		}
	}
	ctxcapExpectStatus(t, as(testUserID, http.MethodPut, offersPath, map[string]any{
		"connector_ids": got.Offers.ConnectorIDs, "skill_ids": got.Offers.SkillIDs,
	}), http.StatusOK, "save round-trip after an orphaned offer")
	var stale int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM context_capability_binding WHERE agent_id = $1 AND resource_id = $2`, agentID, orphan).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("orphaned offer rows=%d err=%v", stale, err)
	}
}

// ctxcapFakeDingTalk is a DingTalk client with H5 JSAPI support.
type ctxcapFakeDingTalk struct {
	supported bool
	ticket    string
	chats     map[string]string
}

func (f *ctxcapFakeDingTalk) IsConfigured() bool { return true }
func (f *ctxcapFakeDingTalk) SearchUsers(context.Context, string, int) ([]dingtalk.User, error) {
	return nil, nil
}
func (f *ctxcapFakeDingTalk) AddGroupMembers(context.Context, string, []string) error { return nil }
func (f *ctxcapFakeDingTalk) JSAPISupported() bool                                    { return f.supported }
func (f *ctxcapFakeDingTalk) JSAPITicket(context.Context) (string, error) {
	if !f.supported {
		return "", dingtalk.ErrUnsupported
	}
	return f.ticket, nil
}
func (f *ctxcapFakeDingTalk) ConvertChatIDToOpenConversationID(_ context.Context, chatID string) (string, error) {
	if !f.supported {
		return "", dingtalk.ErrUnsupported
	}
	cid, ok := f.chats[chatID]
	if !ok {
		return "", errors.New("unknown chat")
	}
	return cid, nil
}

func TestContextCapabilitiesSceneResolveViaJSAPI(t *testing.T) {
	f := newCtxcapFixture(t)
	router := ctxcapRouter(f.h)
	agentID := uuidToString(f.agent)
	alice := uuid.NewString()
	f.cleanupGrantsAndLinks(t)
	f.h.DingTalk = &ctxcapFakeDingTalk{supported: true, ticket: "ticket", chats: map[string]string{
		"chat-other": ctxcapOtherScene, "chat-unknown": "cidNeverServed==",
	}}
	if _, err := testPool.Exec(context.Background(), `INSERT INTO scene_memory (workspace_id, agent_id, org_id, scene_key, scene_kind, scene_title)
		VALUES ($1, $2, $3, $4, 'group', 'Other group')`, testWorkspaceID, agentID, ctxcapOrg, ctxcapOtherScene); err != nil {
		t.Fatal(err)
	}
	resolvePath := "/api/context-capabilities/agents/" + agentID + "/scenes/resolve"

	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPost, resolvePath, alice, map[string]any{"chat_id": "chat-other"}), http.StatusForbidden, "no person grant")
	f.grant(t, alice, contextcap.ScopePerson, ctxcapStaff, "Alice")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPost, resolvePath, alice, map[string]any{}), http.StatusBadRequest, "empty body")
	// A bare openConversationId is not proof of membership: it is not a
	// secret (every scene-grant holder sees it), so only a picked chatId counts.
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPost, resolvePath, alice, map[string]any{"open_conversation_id": ctxcapOtherScene}), http.StatusBadRequest, "bare open_conversation_id")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPost, resolvePath, alice, map[string]any{"chat_id": "chat-unknown"}), http.StatusForbidden, "group the agent never served")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPost, resolvePath, alice, map[string]any{"chat_id": "chat-missing"}), http.StatusBadGateway, "conversion failure")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPost, resolvePath, alice, map[string]any{"chat_id": "chat-other", "open_conversation_id": ctxcapScene}), http.StatusBadRequest, "mismatched ids")

	w := ctxcapMobile(t, router, http.MethodPost, resolvePath, alice, map[string]any{"chat_id": "chat-other"})
	ctxcapExpectStatus(t, w, http.StatusOK, "resolve")
	var resolved struct {
		Scene contextCapSceneDTO `json:"scene"`
	}
	ctxcapDecode(t, w, &resolved)
	if resolved.Scene.ScopeKey != ctxcapOtherScene || resolved.Scene.ScopeTitle != "Other group" || resolved.Scene.Source != contextcap.GrantSourceJSAPI {
		t.Fatalf("resolved scene=%+v", resolved.Scene)
	}
	expires, err := time.Parse(time.RFC3339, resolved.Scene.ExpiresAt)
	if err != nil || expires.Before(time.Now().Add(contextcap.GrantTTLScene-time.Hour)) {
		t.Fatalf("expires_at=%q err=%v", resolved.Scene.ExpiresAt, err)
	}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, ctxcapOtherScene), alice, nil), http.StatusOK, "granted scene detail")

	// The agent detail advertises the picker once H5 signing is configured,
	// which includes an app origin to restrict signed pages to.
	t.Setenv(dingTalkH5CorpIDEnv, "ding-corp")
	t.Setenv(dingTalkH5AgentIDEnv, "12345")
	detailPath := "/api/context-capabilities/agents/" + agentID
	var detail ctxcapAgentDetail
	ctxcapDecode(t, ctxcapMobile(t, router, http.MethodGet, detailPath, alice, nil), &detail)
	if detail.JSAPIAvailable {
		t.Fatal("jsapi_available=true without an app origin")
	}
	f.h.cfg.AppURL = "https://app.multica.example"
	w = ctxcapMobile(t, router, http.MethodGet, detailPath, alice, nil)
	ctxcapDecode(t, w, &detail)
	if !detail.JSAPIAvailable {
		t.Fatalf("jsapi_available=false: %s", w.Body.String())
	}

	// Private-agent mode cannot convert chat ids.
	f.h.DingTalk = &ctxcapFakeDingTalk{supported: false}
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPost, resolvePath, alice, map[string]any{"chat_id": "chat-other"}), http.StatusServiceUnavailable, "unsupported client")
}

// An agent without agent_dingtalk_identity keeps its grants and bindings under
// org "", while scene memory records its groups under the dispatch's DWS org.
// The JSAPI path must still recognise those groups.
func TestContextCapabilitiesSceneResolveWithoutDingTalkIdentity(t *testing.T) {
	f := newCtxcapFixture(t)
	router := ctxcapRouter(f.h)
	agentID := uuidToString(f.agent)
	alice := uuid.NewString()
	f.cleanupGrantsAndLinks(t)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `DELETE FROM agent_dingtalk_identity WHERE agent_id = $1`, agentID); err != nil {
		t.Fatal(err)
	}
	f.h.DingTalk = &ctxcapFakeDingTalk{supported: true, ticket: "ticket", chats: map[string]string{"chat-other": ctxcapOtherScene}}
	if _, err := testPool.Exec(ctx, `INSERT INTO scene_memory (workspace_id, agent_id, org_id, scene_key, scene_kind, scene_title)
		VALUES ($1, $2, 'dws-org-fallback', $3, 'group', 'Other group')`, testWorkspaceID, agentID, ctxcapOtherScene); err != nil {
		t.Fatal(err)
	}
	if _, err := contextcap.UpsertGrant(ctx, testPool, contextcap.Grant{
		UserID: alice, WorkspaceID: testWorkspaceID, AgentID: agentID, ScopeType: contextcap.ScopePerson,
		OrgID: "", ScopeKey: ctxcapStaff, Source: contextcap.GrantSourceAgentLink,
	}, contextcap.GrantTTLPerson); err != nil {
		t.Fatal(err)
	}
	w := ctxcapMobile(t, router, http.MethodPost, "/api/context-capabilities/agents/"+agentID+"/scenes/resolve", alice, map[string]any{"chat_id": "chat-other"})
	ctxcapExpectStatus(t, w, http.StatusOK, "resolve without DingTalk identity")
	var resolved struct {
		Scene contextCapSceneDTO `json:"scene"`
	}
	ctxcapDecode(t, w, &resolved)
	if resolved.Scene.ScopeKey != ctxcapOtherScene || resolved.Scene.ScopeTitle != "Other group" {
		t.Fatalf("resolved scene=%+v", resolved.Scene)
	}
	grant, err := contextcap.GetLiveGrant(ctx, testPool, alice, agentID, contextcap.ScopeScene, "", ctxcapOtherScene)
	if err != nil || grant.Source != contextcap.GrantSourceJSAPI {
		t.Fatalf("scene grant under org \"\" = %+v err=%v", grant, err)
	}
}

func TestDingTalkJSAPIConfigSignsAppPages(t *testing.T) {
	f := newCtxcapFixture(t)
	router := ctxcapRouter(f.h)
	user := uuid.NewString()
	get := func(pageURL string) *httptest.ResponseRecorder {
		return ctxcapMobile(t, router, http.MethodGet, "/api/dingtalk/jsapi-config?url="+url.QueryEscape(pageURL), user, nil)
	}
	page := "https://app.multica.example/dingtalk/configure?agent=" + uuidToString(f.agent)

	ctxcapExpectStatus(t, get(page), http.StatusServiceUnavailable, "unconfigured")
	t.Setenv(dingTalkH5CorpIDEnv, "ding-corp")
	t.Setenv(dingTalkH5AgentIDEnv, "12345")
	ctxcapExpectStatus(t, get(page), http.StatusServiceUnavailable, "no JSAPI client")
	f.h.DingTalk = &ctxcapFakeDingTalk{supported: false}
	ctxcapExpectStatus(t, get(page), http.StatusServiceUnavailable, "private agent client")

	// Without an app origin the signer fails closed instead of signing any
	// page (the fixture only sets PublicURL).
	f.h.DingTalk = &ctxcapFakeDingTalk{supported: true, ticket: "jsapi-ticket-1"}
	ctxcapExpectStatus(t, get("https://evil.example.test/page"), http.StatusServiceUnavailable, "no app origin")
	ctxcapExpectStatus(t, get(page), http.StatusServiceUnavailable, "no app origin, own page")

	f.h.cfg.AppURL = "https://app.multica.example"
	w := get(page)
	ctxcapExpectStatus(t, w, http.StatusOK, "sign")
	if strings.Contains(w.Body.String(), "jsapi-ticket-1") {
		t.Fatal("response leaks the jsapi ticket")
	}
	var cfg map[string]string
	ctxcapDecode(t, w, &cfg)
	if cfg["corp_id"] != "ding-corp" || cfg["agent_id"] != "12345" || cfg["nonce_str"] == "" || cfg["time_stamp"] == "" {
		t.Fatalf("config=%+v", cfg)
	}
	sum := sha1.Sum([]byte("jsapi_ticket=jsapi-ticket-1&noncestr=" + cfg["nonce_str"] + "&timestamp=" + cfg["time_stamp"] + "&url=" + page))
	if cfg["signature"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("signature=%q", cfg["signature"])
	}

	for _, bad := range []string{
		"", "https://evil.example.test/dingtalk/configure", page + "#frag", "javascript:alert(1)", "/dingtalk/configure",
		"https://user@app.multica.example/dingtalk/configure",
	} {
		ctxcapExpectStatus(t, get(bad), http.StatusBadRequest, "bad url "+bad)
	}

	// Task tokens are not human sessions.
	req := httptest.NewRequest(http.MethodGet, "/api/dingtalk/jsapi-config?url="+url.QueryEscape(page), nil)
	req.Header.Set("X-User-ID", user)
	req.Header.Set("X-Actor-Source", "task_token")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	ctxcapExpectStatus(t, w, http.StatusForbidden, "task token")
}

func TestDingTalkJSAPISignedURLDecodesQuery(t *testing.T) {
	h := &Handler{cfg: Config{FrontendOrigin: "https://app.multica.example"}}
	got, err := h.dingTalkJSAPISignedURL("https://APP.multica.example/dingtalk/configure?title=%E7%BE%A4&x=1")
	if err != nil || got != "https://APP.multica.example/dingtalk/configure?title=群&x=1" {
		t.Fatalf("signed url=%q err=%v", got, err)
	}
}
