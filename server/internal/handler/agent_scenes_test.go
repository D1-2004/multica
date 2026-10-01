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
	"github.com/multica-ai/multica/server/internal/service/scenememory"
)

const (
	scenesMemoryKey = "cidScenesMemory=="
	scenesDirectKey = "cidScenesDirect=="
	scenesStaleKey  = "cidScenesStaleOrg=="
	scenesOtherJob  = "cidScenesOtherOrgJob=="
)

// scenesRouter mirrors the admin scene and mobile context capability wiring
// in cmd/server/router.go.
func scenesRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Route("/api/context-capabilities", func(r chi.Router) {
		r.Use(RequireDingTalkHumanActor)
		r.Post("/links/redeem", h.RedeemContextConfigLink)
		r.Get("/agents/{agentId}", h.GetContextConfigAgent)
		r.Get("/agents/{agentId}/scenes/{sceneKey}", h.GetContextConfigScene)
		r.Put("/agents/{agentId}/bindings", h.PutContextConfigBinding)
		r.Put("/agents/{agentId}/credentials", h.PutContextConfigCredential)
		r.Delete("/agents/{agentId}/credentials", h.DeleteContextConfigCredential)
	})
	r.Route("/api/agents/{id}/tenants", func(r chi.Router) {
		r.Use(RequireHumanActor)
		r.Get("/", h.ListAgentTenants)
		r.Post("/", h.CreateAgentTenant)
		r.Patch("/{orgId}", h.RenameAgentTenant)
		r.Delete("/{orgId}", h.DeleteAgentTenant)
		r.Get("/{orgId}/groups", h.ListAgentTenantGroups)
		r.Get("/{orgId}/persons", h.ListAgentTenantPersons)
		r.Get("/{orgId}/context/{scopeType}/{scopeKey}", h.GetAgentContextNode)
		r.Put("/{orgId}/context/{scopeType}/{scopeKey}/bindings", h.PutAgentContextBinding)
		r.Put("/{orgId}/context/{scopeType}/{scopeKey}/prompts", h.PutAgentContextPrompts)
		r.Put("/{orgId}/context/{scopeType}/{scopeKey}/mcp-config", h.PutAgentContextMCPConfig)
		r.Put("/{orgId}/context/{scopeType}/{scopeKey}/credentials", h.PutAgentContextCredential)
		r.Delete("/{orgId}/context/{scopeType}/{scopeKey}/credentials", h.DeleteAgentContextCredential)
		r.Post("/{orgId}/context/{scopeType}/{scopeKey}/connections/start", h.StartAgentContextConnection)
		r.Delete("/{orgId}/context/{scopeType}/{scopeKey}/grants", h.RevokeAgentContextGrants)
	})
	return r
}

type scenesListResponse struct {
	Scenes  []agentSceneDTO `json:"scenes"`
	HasMore bool            `json:"has_more"`
}

// tenantGroupsPath is the group list route of one tenant of agentID.
func tenantGroupsPath(agentID, orgID string) string {
	return "/api/agents/" + agentID + "/tenants/" + url.PathEscape(orgID) + "/groups"
}

// tenantGroups lists the group scenes of the fixture's identity tenant as
// the handler test user (a workspace owner); query is an optional
// "limit=&offset=" string.
func tenantGroups(t *testing.T, router http.Handler, agentID, query string) scenesListResponse {
	t.Helper()
	path := tenantGroupsPath(agentID, ctxcapOrg)
	if query != "" {
		path += "?" + query
	}
	w := scenesAs(t, router, "", http.MethodGet, path, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "tenant groups "+query)
	var list scenesListResponse
	ctxcapDecode(t, w, &list)
	return list
}

// ctxNodePath is the Context Builder path of one node of the fixture org,
// with the key escaped like the web client's encodeURIComponent.
func ctxNodePath(agentID, orgID, scopeType, key string) string {
	escape := func(v string) string { return strings.ReplaceAll(url.QueryEscape(v), "+", "%20") }
	return "/api/agents/" + agentID + "/tenants/" + escape(orgID) + "/context/" + scopeType + "/" + escape(key)
}

// ctxNode reads one Context Builder node as userID ("" = the handler test
// user, a workspace owner).
func ctxNode(t *testing.T, router http.Handler, userID, agentID, scopeType, key string) agentContextNodeResponse {
	t.Helper()
	w := scenesAs(t, router, userID, http.MethodGet, ctxNodePath(agentID, ctxcapOrg, scopeType, key), nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "context node "+scopeType+" "+key)
	var node agentContextNodeResponse
	ctxcapDecode(t, w, &node)
	return node
}

// ctxNodeConnector returns the offered connector id of a node.
func ctxNodeConnector(t *testing.T, node agentContextNodeResponse, connectorID string) agentContextConnectorDTO {
	t.Helper()
	for _, c := range node.Connectors {
		if c.ID == connectorID {
			return c
		}
	}
	t.Fatalf("connector %s not offered in %+v", connectorID, node.Connectors)
	return agentContextConnectorDTO{}
}

// ctxNodeEnabled lists the ids a node switches on (connectors and skills).
func ctxNodeEnabled(node agentContextNodeResponse) map[string]bool {
	out := map[string]bool{}
	for _, c := range node.Connectors {
		if c.Enabled {
			out[c.ID] = true
		}
	}
	for _, s := range node.Skills {
		if s.Enabled {
			out[s.ID] = true
		}
	}
	return out
}

func scenesAs(t *testing.T, router http.Handler, userID, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := newRequest(method, path, body)
	if userID != "" {
		req.Header.Set("X-User-ID", userID)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// sceneMemoryRow plants a scene_memory row updated age ago.
func (f *ctxcapFixture) sceneMemoryRow(t *testing.T, orgID, key, kind, title string, age time.Duration) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `INSERT INTO scene_memory
		(workspace_id, agent_id, platform, org_id, scene_key, scene_kind, scene_title, memory_text, updated_at)
		VALUES ($1, $2, 'dingtalk', $3, $4, $5, $6, 'notes', now() - make_interval(secs => $7::double precision))`,
		testWorkspaceID, uuidToString(f.agent), orgID, key, kind, title, age.Seconds()); err != nil {
		t.Fatal(err)
	}
}

// coordinatorJob plants one inbound Coordinator job with its chat session
// updated age ago and returns the session id.
func (f *ctxcapFixture) coordinatorJob(t *testing.T, cid, conversationType, title, sender, dispatchOrg string, age time.Duration) string {
	t.Helper()
	return f.coordinatorJobFrom(t, testWorkspaceID, "digital_employee", cid, conversationType, title, sender, dispatchOrg, age)
}

// coordinatorJobFrom is coordinatorJob under an explicit endpoint namespace
// and source type (the transcript partition of the conversation).
func (f *ctxcapFixture) coordinatorJobFrom(t *testing.T, endpointNamespace, sourceType, cid, conversationType, title, sender, dispatchOrg string, age time.Duration) string {
	t.Helper()
	return f.insertCoordinatorJob(t, endpointNamespace, sourceType, cid, conversationType, title, map[string]any{"displayName": sender}, dispatchOrg, age)
}

// coordinatorDMJob plants a 1:1 chat Coordinator job whose sender carries a
// staffId (the person a DM scene is bound to), under the fixture's org.
func (f *ctxcapFixture) coordinatorDMJob(t *testing.T, cid, sender, staffID string, age time.Duration) string {
	t.Helper()
	return f.insertCoordinatorJob(t, testWorkspaceID, "digital_employee", cid, "single", "",
		map[string]any{"displayName": sender, "staffId": staffID}, ctxcapOrg, age)
}

func (f *ctxcapFixture) insertCoordinatorJob(t *testing.T, endpointNamespace, sourceType, cid, conversationType, title string, sender map[string]any, dispatchOrg string, age time.Duration) string {
	t.Helper()
	ctx := context.Background()
	command := map[string]any{
		"source": map[string]any{"platform": "dingtalk", "type": sourceType},
		"event": map[string]any{"data": map[string]any{
			"conversation": map[string]any{"openConversationId": cid, "type": conversationType, "title": title},
			"sender":       sender,
		}},
		"externalIdentity": map[string]any{"dws": map[string]any{"orgId": dispatchOrg}},
	}
	raw, _ := json.Marshal(command)
	var sessionID string
	if err := testPool.QueryRow(ctx, `INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, updated_at)
		VALUES ($1, $2, $3, 'coordinator', now() - make_interval(secs => $4::double precision)) RETURNING id::text`,
		testWorkspaceID, uuidToString(f.agent), testUserID, age.Seconds()).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO inbound_coordinator_job
		(acceptance_id, workspace_id, agent_id, user_id, endpoint_namespace_id, idempotency_key, command, chat_session_id, user_message_id, status)
		VALUES ($1, $2, $3, $4, $9, $5, $6, $7, $8, 'completed')`,
		uuid.NewString(), testWorkspaceID, uuidToString(f.agent), testUserID, uuid.NewString(), raw, sessionID, uuid.NewString(), endpointNamespace); err != nil {
		t.Fatal(err)
	}
	return sessionID
}

func (f *ctxcapFixture) cleanupScenes(t *testing.T) {
	t.Helper()
	agentID := uuidToString(f.agent)
	t.Cleanup(func() {
		bg := context.Background()
		for _, statement := range []string{
			`DELETE FROM inbound_coordinator_job WHERE agent_id = $1`,
			`DELETE FROM chat_session WHERE agent_id = $1`,
			`DELETE FROM scene_memory WHERE agent_id = $1`,
			`DELETE FROM agent_scene_config WHERE agent_id = $1`,
			`DELETE FROM context_prompt_component WHERE agent_id = $1`,
			`DELETE FROM context_scope_mcp_config WHERE agent_id = $1`,
			`DELETE FROM agent_tenant WHERE agent_id = $1`,
		} {
			_, _ = testPool.Exec(bg, statement, agentID)
		}
	})
}

func scenesByKey(scenes []agentSceneDTO) map[string]agentSceneDTO {
	out := map[string]agentSceneDTO{}
	for _, scene := range scenes {
		out[scene.SceneKey] = scene
	}
	return out
}

func TestAgentScenesListMergesSourcesUnderCurrentOrg(t *testing.T) {
	f := newCtxcapFixture(t)
	f.cleanupScenes(t)
	router := scenesRouter(f.h)
	agentID := uuidToString(f.agent)

	// Group scene known from scene memory and a Coordinator conversation.
	f.sceneMemoryRow(t, ctxcapOrg, scenesMemoryKey, "group", "Memory group", 3*time.Hour)
	memorySession := f.coordinatorJob(t, scenesMemoryKey, "group", "Memory group", "Bob", ctxcapOrg, 30*time.Minute)
	// 1:1 scene known only from two Coordinator sessions.
	f.coordinatorJob(t, scenesDirectKey, "single", "", "Alice", "", 2*time.Hour)
	directSession := f.coordinatorJob(t, scenesDirectKey, "single", "", "Alice", "", time.Hour)
	// Rows of another org are not scenes of the agent's current binding.
	f.sceneMemoryRow(t, "org-other", scenesStaleKey, "group", "Old org group", time.Minute)
	f.coordinatorJob(t, scenesOtherJob, "group", "Old org job", "Bob", "org-other", time.Minute)
	// ctxcapScene is known only from its fixture bindings (updated just now).

	// The tenant's group list leaves the 1:1 chat out.
	list := tenantGroups(t, router, agentID, "")
	if list.HasMore || len(list.Scenes) != 2 {
		t.Fatalf("groups=%+v has_more=%v", list.Scenes, list.HasMore)
	}
	if list.Scenes[0].SceneKey != ctxcapScene || list.Scenes[1].SceneKey != scenesMemoryKey {
		t.Fatalf("groups not ordered by last activity: %+v", list.Scenes)
	}
	byKey := scenesByKey(list.Scenes)
	bound := byKey[ctxcapScene]
	// Known from its bindings alone (TestAgentContextNodePromptsAndBindings
	// covers which bindings the node lists).
	if bound.Kind != "group" || bound.MemoryID != "" || bound.InboundSessionID != "" || bound.InboundCount != 0 ||
		bound.HasPrompt || bound.OrgID != ctxcapOrg || bound.LastActiveAt == "" {
		t.Fatalf("binding-only scene=%+v", bound)
	}
	memory := byKey[scenesMemoryKey]
	if memory.Kind != "group" || memory.Title != "Memory group" || memory.MemoryID == "" || memory.InboundSessionID != memorySession ||
		memory.InboundCount != 1 {
		t.Fatalf("memory scene=%+v", memory)
	}
	// The 1:1 chat is still a scene of the tenant: its node reads it (its
	// person is unknown, so it has no configuration).
	directNode := ctxNode(t, router, "", agentID, contextcap.ScopeScene, scenesDirectKey)
	direct := directNode.Scene
	if direct == nil || direct.Kind != "dm" || direct.Title != "Alice" || direct.InboundSessionID != directSession || direct.InboundCount != 2 ||
		direct.MemoryID != "" || directNode.Scope != nil {
		t.Fatalf("direct scene=%+v scope=%+v", direct, directNode.Scope)
	}
	// Scenes of another org are not scenes of this tenant.
	for _, key := range []string{scenesStaleKey, scenesOtherJob} {
		ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodGet, ctxNodePath(agentID, ctxcapOrg, contextcap.ScopeScene, key), nil),
			http.StatusNotFound, "scene of another org "+key)
	}

	// Pagination.
	list = tenantGroups(t, router, agentID, "limit=1")
	if !list.HasMore || len(list.Scenes) != 1 || list.Scenes[0].SceneKey != ctxcapScene {
		t.Fatalf("first page=%+v has_more=%v", list.Scenes, list.HasMore)
	}
	list = tenantGroups(t, router, agentID, "limit=1&offset=1")
	if list.HasMore || len(list.Scenes) != 1 || list.Scenes[0].SceneKey != scenesMemoryKey {
		t.Fatalf("second page=%+v has_more=%v", list.Scenes, list.HasMore)
	}
	groupsPath := tenantGroupsPath(agentID, ctxcapOrg)
	for _, bad := range []string{"limit=0", "limit=201", "limit=x", "offset=-1"} {
		ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodGet, groupsPath+"?"+bad, nil), http.StatusBadRequest, bad)
	}

	// The inbound session opens that conversation's transcript.
	messagesRouter := chi.NewRouter()
	messagesRouter.Get("/api/agents/{id}/coordinator-conversations/{sessionId}/messages", f.h.ListAgentCoordinatorConversationMessages)
	ctxcapExpectStatus(t, scenesAs(t, messagesRouter, "", http.MethodGet,
		"/api/agents/"+agentID+"/coordinator-conversations/"+directSession+"/messages", nil), http.StatusOK, "scene transcript")

	// Only human workspace owners/admins or the agent owner.
	if w := scenesAs(t, router, uuid.NewString(), http.MethodGet, groupsPath, nil); w.Code == http.StatusOK {
		t.Fatalf("outsider listed scenes: %s", w.Body.String())
	}
	member := createPermissionTestMember(t, "scenes-member-"+uuid.NewString()[:8]+"@example.test")
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE user_id = $1`, member) })
	ctxcapExpectStatus(t, scenesAs(t, router, member, http.MethodGet, groupsPath, nil), http.StatusForbidden, "plain member")
	taskActor := newRequest(http.MethodGet, groupsPath, nil)
	taskActor.Header.Set("X-Actor-Source", "task_token")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, taskActor)
	ctxcapExpectStatus(t, rec, http.StatusForbidden, "task token")
}

func TestAgentContextNodePromptsAndBindings(t *testing.T) {
	f := newCtxcapFixture(t)
	f.cleanupScenes(t)
	router := scenesRouter(f.h)
	agentID := uuidToString(f.agent)
	nodePath := ctxNodePath(agentID, ctxcapOrg, contextcap.ScopeScene, ctxcapScene)

	w := scenesAs(t, router, "", http.MethodGet, nodePath, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "scene node")
	var node agentContextNodeResponse
	ctxcapDecode(t, w, &node)
	if node.Scene == nil || node.Scene.SceneKey != ctxcapScene || len(node.Prompts) != 0 || node.Scope == nil ||
		*node.Scope != (agentContextScopeDTO{Type: contextcap.ScopeScene, OrgID: ctxcapOrg, Key: ctxcapScene, Title: node.Scene.Title}) {
		t.Fatalf("node scene=%+v scope=%+v prompts=%+v", node.Scene, node.Scope, node.Prompts)
	}
	if enabled := ctxNodeEnabled(node); len(enabled) != 2 || !enabled[f.scene] || !enabled[f.skillScene] {
		t.Fatalf("node switches=%v, want only the offered scene connector and skill", enabled)
	}
	// The offered connectors and the granted one (通用能力, for the group's own account).
	if len(node.Connectors) != 3 || ctxNodeConnector(t, node, f.scene).AuthMode != "bearer" || ctxNodeConnector(t, node, f.person).AuthMode != "none" ||
		!ctxNodeConnector(t, node, f.global).Global ||
		len(node.Skills) != 1 || node.Skills[0].ID != f.skillScene {
		t.Fatalf("node offers connectors=%+v skills=%+v", node.Connectors, node.Skills)
	}
	if strings.Contains(w.Body.String(), "upstream_url") || strings.Contains(w.Body.String(), "workspace-secret") {
		t.Fatalf("context node leaks connector internals: %s", w.Body.String())
	}

	// Prompt components: trimmed, stored with the actor's name, reflected in
	// the list; the whole list is replaced on every save.
	promptsPath := nodePath + "/prompts"
	w = scenesAs(t, router, "", http.MethodPut, promptsPath, map[string]any{"prompts": []map[string]any{
		{"name": " 语气 ", "order": 1, "text": "  Be concise in this group.  "},
		{"name": "规则", "order": 0, "text": "Cite sources."},
	}})
	ctxcapExpectStatus(t, w, http.StatusOK, "put prompts")
	var promptsResp struct {
		Prompts []agentContextPromptDTO `json:"prompts"`
	}
	ctxcapDecode(t, w, &promptsResp)
	if len(promptsResp.Prompts) != 2 || promptsResp.Prompts[0].Name != "规则" || promptsResp.Prompts[1].Name != "语气" ||
		promptsResp.Prompts[1].Text != "Be concise in this group." || promptsResp.Prompts[1].UpdatedByName != handlerTestName ||
		promptsResp.Prompts[1].UpdatedAt == "" || promptsResp.Prompts[1].ID == "" {
		t.Fatalf("prompts=%+v", promptsResp.Prompts)
	}
	if list := tenantGroups(t, router, agentID, ""); !scenesByKey(list.Scenes)[ctxcapScene].HasPrompt {
		t.Fatalf("list does not report the prompts: %+v", list.Scenes)
	}
	for name, body := range map[string]any{
		"8001 characters": map[string]any{"prompts": []map[string]any{{"name": "a", "text": strings.Repeat("a", contextcap.MaxScenePrompt+1)}}},
		"NUL byte":        map[string]any{"prompts": []map[string]any{{"name": "a", "text": "a\x00b"}}},
		"empty text":      map[string]any{"prompts": []map[string]any{{"name": "a", "text": " "}}},
		"empty name":      map[string]any{"prompts": []map[string]any{{"name": "", "text": "x"}}},
		"missing prompts": map[string]any{},
		"unknown field":   map[string]any{"prompts": []map[string]any{}, "kind": "dm"},
	} {
		ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, promptsPath, body), http.StatusBadRequest, name)
	}
	w = scenesAs(t, router, "", http.MethodPut, promptsPath, map[string]any{"prompts": []map[string]any{{"name": "a", "text": "x"}, {"name": "a", "text": "y"}}})
	if w.Code != http.StatusBadRequest || catalogErrorCode(t, w) != agentContextErrDuplicate {
		t.Fatalf("duplicate names: %d %s", w.Code, w.Body.String())
	}
	tooMany := []map[string]any{}
	for i := 0; i <= contextcap.MaxPromptComponents; i++ {
		tooMany = append(tooMany, map[string]any{"name": "p" + strings.Repeat("x", i), "text": "t"})
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, promptsPath, map[string]any{"prompts": tooMany}), http.StatusBadRequest, "21 prompts")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, promptsPath, map[string]any{"prompts": []map[string]any{{"name": "a", "text": strings.Repeat("场", contextcap.MaxScenePrompt)}}}), http.StatusOK, "8000 characters")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, ctxNodePath(agentID, ctxcapOrg, contextcap.ScopeScene, "cidNeverSeen==")+"/prompts", map[string]any{"prompts": []map[string]any{}}), http.StatusNotFound, "unknown scene")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, "/api/agents/"+agentID+"/tenants/"+ctxcapOrg+"/context/scene/not-a-cid/prompts", map[string]any{"prompts": []map[string]any{}}), http.StatusBadRequest, "malformed scene key")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, ctxNodePath(agentID, "org-not-a-tenant", contextcap.ScopeScene, ctxcapScene)+"/prompts", map[string]any{"prompts": []map[string]any{}}), http.StatusNotFound, "unknown tenant")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, promptsPath, map[string]any{"prompts": []map[string]any{}}), http.StatusOK, "clear prompts")

	// The agent owner may write them; a plain member may not.
	owner := createPermissionTestMember(t, "scenes-owner-"+uuid.NewString()[:8]+"@example.test")
	member := createPermissionTestMember(t, "scenes-plain-"+uuid.NewString()[:8]+"@example.test")
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE user_id = ANY($1::uuid[])`, []string{owner, member})
	})
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET owner_id = $1 WHERE id = $2`, owner, agentID); err != nil {
		t.Fatal(err)
	}
	ownerPrompts := map[string]any{"prompts": []map[string]any{{"name": "owner", "text": "owner"}}}
	ctxcapExpectStatus(t, scenesAs(t, router, member, http.MethodPut, promptsPath, ownerPrompts), http.StatusForbidden, "plain member prompts")
	w = scenesAs(t, router, owner, http.MethodPut, promptsPath, ownerPrompts)
	ctxcapExpectStatus(t, w, http.StatusOK, "agent owner prompts")
	ctxcapDecode(t, w, &promptsResp)
	if len(promptsResp.Prompts) != 1 || !strings.HasPrefix(promptsResp.Prompts[0].UpdatedByName, "scenes-owner-") {
		t.Fatalf("owner prompts=%+v", promptsResp.Prompts)
	}

	// Binding toggles are offer-gated like the mobile PUT.
	bindingsPath := nodePath + "/bindings"
	w = scenesAs(t, router, "", http.MethodPut, bindingsPath, map[string]any{"resource_type": "connector", "resource_id": f.person, "enabled": true})
	ctxcapExpectStatus(t, w, http.StatusOK, "admin enables an offered connector")
	var bindingResp struct {
		Binding agentSceneBindingDTO `json:"binding"`
	}
	ctxcapDecode(t, w, &bindingResp)
	if !bindingResp.Binding.Enabled || bindingResp.Binding.ResourceID != f.person || bindingResp.Binding.UpdatedByName != handlerTestName || bindingResp.Binding.UpdatedAt == "" {
		t.Fatalf("binding=%+v", bindingResp.Binding)
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, bindingsPath, map[string]any{"resource_type": "connector", "resource_id": f.scene, "enabled": false}), http.StatusOK, "admin disables")
	for name, body := range map[string]map[string]any{
		"unoffered connector": {"resource_type": "connector", "resource_id": f.notOffered, "enabled": true},
		"granted only":        {"resource_type": "connector", "resource_id": f.global, "enabled": true},
		"unoffered skill":     {"resource_type": "skill", "resource_id": f.skillFree, "enabled": false},
	} {
		ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, bindingsPath, body), http.StatusForbidden, name)
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, bindingsPath, map[string]any{"resource_type": "connector", "resource_id": f.person, "enabled": true, "share_in_groups": true}), http.StatusBadRequest, "share_in_groups on a scene")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, bindingsPath, map[string]any{"resource_type": "connector", "resource_id": "nope", "enabled": true}), http.StatusBadRequest, "malformed resource id")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, ctxNodePath(agentID, ctxcapOrg, contextcap.ScopeScene, "cidNeverSeen==")+"/bindings", map[string]any{"resource_type": "connector", "resource_id": f.person, "enabled": true}), http.StatusNotFound, "unknown scene binding")
	ctxcapExpectStatus(t, scenesAs(t, router, member, http.MethodPut, bindingsPath, map[string]any{"resource_type": "connector", "resource_id": f.person, "enabled": false}), http.StatusForbidden, "plain member binding")

	// The mobile page of the group sees the admin's toggles.
	alice := uuid.NewString()
	f.grant(t, alice, contextcap.ScopeScene, ctxcapScene, "Ctxcap group")
	w = ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, ctxcapScene), alice, nil)
	var mobile ctxcapSceneDetail
	ctxcapDecode(t, w, &mobile)
	if !ctxcapHasBinding(mobile.Bindings, f.person, true) || !ctxcapHasBinding(mobile.Bindings, f.scene, false) || mobile.Scene.Kind != "group" {
		t.Fatalf("mobile scene after admin toggles=%+v", mobile)
	}
}

func TestAgentScenesEscapedKeyAndMemoryOnlyScene(t *testing.T) {
	f := newCtxcapFixture(t)
	f.cleanupScenes(t)
	router := scenesRouter(f.h)
	agentID := uuidToString(f.agent)
	const slashy = "cidScenes+Ab/Cd=="
	// scene_memory records "dm" for any chat type that is not "group", so a
	// memory-only scene is not proof of a 1:1 chat.
	f.sceneMemoryRow(t, ctxcapOrg, slashy, "dm", "", time.Minute)
	path := ctxNodePath(agentID, ctxcapOrg, contextcap.ScopeScene, slashy)
	if !strings.Contains(path, "%2F") || !strings.Contains(path, "%2B") {
		t.Fatalf("path %q does not exercise escaping", path)
	}
	node := ctxNode(t, router, "", agentID, contextcap.ScopeScene, slashy)
	if node.Scene == nil || node.Scene.SceneKey != slashy || node.Scene.Kind != "group" || node.Scene.MemoryID == "" {
		t.Fatalf("scene=%+v", node.Scene)
	}
	w := scenesAs(t, router, "", http.MethodPut, path+"/prompts", map[string]any{"prompts": []map[string]any{{"name": "语气", "text": "简短回答"}}})
	ctxcapExpectStatus(t, w, http.StatusOK, "memory-only prompts")
	var stored string
	if err := testPool.QueryRow(context.Background(), `SELECT text FROM context_prompt_component WHERE agent_id = $1 AND scope_type = 'scene' AND scope_key = $2`,
		agentID, slashy).Scan(&stored); err != nil || stored != "简短回答" {
		t.Fatalf("stored prompt=%q err=%v", stored, err)
	}
}

// Scene kind is dm only on positive evidence, and the inbound count matches
// the transcript the inbound session opens.
func TestAgentScenesKindEvidenceAndInboundPartition(t *testing.T) {
	f := newCtxcapFixture(t)
	f.cleanupScenes(t)
	router := scenesRouter(f.h)
	agentID := uuidToString(f.agent)
	const (
		untypedKey = "cidScenesUntyped=="
		singleKey  = "cidScenesSingle=="
		linkedKey  = "cidScenesLinkedDM=="
		splitKey   = "cidScenesSplit=="
	)
	// A dispatch without a conversation type left a "dm" memory row.
	f.sceneMemoryRow(t, ctxcapOrg, untypedKey, "dm", "", time.Hour)
	f.coordinatorJob(t, untypedKey, "", "Untyped group", "Bob", ctxcapOrg, time.Hour)
	// A positively 1:1 conversation.
	f.sceneMemoryRow(t, ctxcapOrg, singleKey, "dm", "", time.Hour)
	f.coordinatorJob(t, singleKey, "single", "", "Carol", ctxcapOrg, time.Hour)
	// A 1:1 chat registered by a personal link, known from memory only.
	f.sceneMemoryRow(t, ctxcapOrg, linkedKey, "dm", "", time.Hour)
	if err := contextcap.RegisterDirectScene(context.Background(), testPool, testWorkspaceID, agentID, ctxcapOrg, linkedKey, "Dora"); err != nil {
		t.Fatal(err)
	}
	// One group reached through two endpoint namespaces: the newest session's
	// transcript covers only its own partition.
	otherNamespace := uuid.NewString()
	f.coordinatorJobFrom(t, otherNamespace, "robot", splitKey, "group", "Split group", "Bob", ctxcapOrg, 4*time.Hour)
	f.coordinatorJobFrom(t, otherNamespace, "robot", splitKey, "group", "Split group", "Bob", ctxcapOrg, 3*time.Hour)
	f.coordinatorJob(t, splitKey, "group", "Split group", "Bob", ctxcapOrg, 2*time.Hour)
	newest := f.coordinatorJob(t, splitKey, "group", "Split group", "Bob", ctxcapOrg, time.Minute)

	// The group list holds the groups only; every scene's node reads its kind.
	byKey := scenesByKey(tenantGroups(t, router, agentID, "").Scenes)
	for key, want := range map[string]string{untypedKey: "group", singleKey: "dm", linkedKey: "dm", splitKey: "group"} {
		if _, listed := byKey[key]; listed != (want == "group") {
			t.Errorf("%s listed=%v as a group, want kind %q", key, listed, want)
		}
		if node := ctxNode(t, router, "", agentID, contextcap.ScopeScene, key); node.Scene == nil || node.Scene.Kind != want {
			t.Errorf("%s node scene=%+v, want kind %q", key, node.Scene, want)
		}
	}
	if split := byKey[splitKey]; split.InboundSessionID != newest || split.InboundCount != 2 {
		t.Fatalf("split scene=%+v, want the newest session and the 2 sessions of its partition", split)
	}
	// The single-key lookup behind nodes and writes agrees with the list.
	node := ctxNode(t, router, "", agentID, contextcap.ScopeScene, splitKey)
	if node.Scene == nil || node.Scene.InboundSessionID != newest || node.Scene.InboundCount != 2 || node.Scene.Kind != "group" {
		t.Fatalf("split node=%+v", node.Scene)
	}
	if node = ctxNode(t, router, "", agentID, contextcap.ScopeScene, untypedKey); node.Scene == nil || node.Scene.Kind != "group" {
		t.Fatalf("untyped node=%+v", node.Scene)
	}
}

// The scene detail opens its memory row by id, whatever its position in the
// capped agent-wide list.
func TestAgentSceneMemoryByID(t *testing.T) {
	f := newCtxcapFixture(t)
	f.cleanupScenes(t)
	f.h.SceneMemoryStore = scenememory.NewStore(f.h.Queries)
	router := chi.NewRouter()
	router.Get("/api/agents/{id}/scene-memory/{memoryId}", f.h.GetAgentSceneMemory)
	agentID := uuidToString(f.agent)
	const key = "cidScenesMemoryByID=="
	f.sceneMemoryRow(t, "org-previous", key, "group", "Old group", 400*time.Hour)
	var memoryID string
	if err := testPool.QueryRow(context.Background(), `SELECT id::text FROM scene_memory WHERE agent_id = $1 AND scene_key = $2`, agentID, key).Scan(&memoryID); err != nil {
		t.Fatal(err)
	}
	w := scenesAs(t, router, "", http.MethodGet, "/api/agents/"+agentID+"/scene-memory/"+memoryID, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "memory by id")
	var memory sceneMemoryResponse
	ctxcapDecode(t, w, &memory)
	if memory.ID != memoryID || memory.SceneKey != key || memory.OrgID != "org-previous" || memory.MemoryText != "notes" {
		t.Fatalf("memory=%+v", memory)
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodGet, "/api/agents/"+agentID+"/scene-memory/"+uuid.NewString(), nil), http.StatusNotFound, "unknown memory")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodGet, "/api/agents/"+agentID+"/scene-memory/not-a-uuid", nil), http.StatusBadRequest, "malformed memory id")
	member := createPermissionTestMember(t, "scenes-memory-"+uuid.NewString()[:8]+"@example.test")
	t.Cleanup(func() { _, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE user_id = $1`, member) })
	ctxcapExpectStatus(t, scenesAs(t, router, member, http.MethodGet, "/api/agents/"+agentID+"/scene-memory/"+memoryID, nil), http.StatusForbidden, "plain member")
}

func TestContextCapabilitiesPersonShareInGroups(t *testing.T) {
	f := newCtxcapFixture(t)
	router := ctxcapRouter(f.h)
	agentID := uuidToString(f.agent)
	alice := uuid.NewString()
	f.grant(t, alice, contextcap.ScopePerson, ctxcapStaff, "Alice")
	// A group grant too: share_in_groups is checked against the scope the
	// request resolves to, after the caller's access to it.
	f.grant(t, alice, contextcap.ScopeScene, ctxcapScene, "Ctxcap group")
	bindingPath := "/api/context-capabilities/agents/" + agentID + "/bindings"
	put := func(body map[string]any) *httptest.ResponseRecorder {
		return ctxcapMobile(t, router, http.MethodPut, bindingPath, alice, body)
	}
	personConnector := func(extra map[string]any) map[string]any {
		body := map[string]any{"scope_type": "person", "scope_key": ctxcapStaff, "resource_type": "connector", "resource_id": f.person, "enabled": true}
		for k, v := range extra {
			body[k] = v
		}
		return body
	}
	type bindingEnvelope struct {
		Binding contextCapBindingDTO `json:"binding"`
	}
	shared := func(w *httptest.ResponseRecorder) *bool {
		t.Helper()
		ctxcapExpectStatus(t, w, http.StatusOK, "person binding")
		var got bindingEnvelope
		ctxcapDecode(t, w, &got)
		return got.Binding.ShareInGroups
	}

	if got := shared(put(personConnector(nil))); got == nil || *got {
		t.Fatalf("default share_in_groups=%v, want present and false", got)
	}
	if got := shared(put(personConnector(map[string]any{"share_in_groups": true}))); got == nil || !*got {
		t.Fatalf("share_in_groups after opt-in=%v", got)
	}
	// Omitting the field keeps the stored value.
	if got := shared(put(personConnector(map[string]any{"enabled": false}))); got == nil || !*got {
		t.Fatalf("share_in_groups after a toggle without it=%v", got)
	}
	if got := shared(put(personConnector(map[string]any{"share_in_groups": false}))); got == nil || *got {
		t.Fatalf("share_in_groups after opt-out=%v", got)
	}
	ctxcapExpectStatus(t, put(map[string]any{"scope_type": "person", "scope_key": ctxcapStaff, "resource_type": "skill", "resource_id": f.skillScene, "enabled": true, "share_in_groups": true}),
		http.StatusBadRequest, "share_in_groups on a skill")
	// A group takes no share_in_groups. Its link holder may not toggle the
	// group at all (403); a manager, who may, gets the 400.
	sceneShare := map[string]any{"scope_type": "scene", "scope_key": ctxcapScene, "resource_type": "connector", "resource_id": f.scene, "enabled": true, "share_in_groups": true}
	ctxcapExpectStatus(t, put(sceneShare), http.StatusForbidden, "share_in_groups on a scene by its link holder")
	f.sceneMemoryRow(t, ctxcapOrg, ctxcapScene, "group", "Ctxcap group", time.Minute)
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPut, bindingPath, testUserID, sceneShare), http.StatusBadRequest, "share_in_groups on a scene")
	ctxcapExpectStatus(t, put(personConnector(map[string]any{"share_in_groups": "yes"})), http.StatusBadRequest, "non-boolean share_in_groups")

	// Person connector bindings carry the field; skill bindings do not.
	if _, err := contextcap.UpsertBinding(context.Background(), testPool, contextcap.BindingWrite{
		WorkspaceID: testWorkspaceID, AgentID: agentID, ScopeType: contextcap.ScopePerson, OrgID: ctxcapOrg, ScopeKey: ctxcapStaff,
		ResourceType: contextcap.ResourceSkill, ResourceID: f.skillScene, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	w := ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID, alice, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "agent detail")
	var raw struct {
		Person struct {
			Bindings []map[string]any `json:"bindings"`
		} `json:"person"`
	}
	ctxcapDecode(t, w, &raw)
	seen := 0
	for _, binding := range raw.Person.Bindings {
		_, has := binding["share_in_groups"]
		switch binding["resource_type"] {
		case "connector":
			seen++
			if !has {
				t.Fatalf("connector binding lacks share_in_groups: %v", binding)
			}
		case "skill":
			seen++
			if has {
				t.Fatalf("skill binding carries share_in_groups: %v", binding)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("person bindings=%v", raw.Person.Bindings)
	}
}

func TestContextCapabilitiesDirectLinkGrantsDMScene(t *testing.T) {
	f := newCtxcapFixture(t)
	f.h.cfg.AppURL = "https://app.multica.example"
	f.cleanupGrantsAndLinks(t)
	f.cleanupScenes(t)
	router := scenesRouter(f.h)
	agentID := uuidToString(f.agent)
	const dmKey = "cidCtxcapDirect=="
	ctx := context.Background()

	// A group scene link and a DM link without a conversation id carry no
	// extra scene.
	groupLink, isError, text := ctxcapToolResult(t, f.ctxcapToolCall(t, f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff)), nil))
	if isError {
		t.Fatalf("group mint: %s", text)
	}
	noCidLink, isError, text := ctxcapToolResult(t, f.ctxcapToolCall(t, f.task(t, ctxcapDispatch("single", "", ctxcapStaff, ctxcapStaff)), nil))
	if isError {
		t.Fatalf("dm mint without cid: %s", text)
	}
	for _, link := range []ctxcapLinkResult{groupLink, noCidLink} {
		var extra string
		if err := testPool.QueryRow(ctx, `SELECT extra_scene_key FROM context_config_link WHERE token_hash = $1`,
			contextcap.HashLinkToken(ctxcapLinkToken(t, link))).Scan(&extra); err != nil || extra != "" {
			t.Fatalf("%s link extra_scene_key=%q err=%v", link.Scope, extra, err)
		}
	}

	dmTask := f.task(t, ctxcapDispatch("single", dmKey, ctxcapStaff, ctxcapStaff))
	dmLink, isError, text := ctxcapToolResult(t, f.ctxcapToolCall(t, dmTask, nil))
	if isError || dmLink.Scope != contextcap.ScopePerson {
		t.Fatalf("dm mint isError=%v text=%q", isError, text)
	}
	token := ctxcapLinkToken(t, dmLink)
	var extra string
	if err := testPool.QueryRow(ctx, `SELECT extra_scene_key FROM context_config_link WHERE token_hash = $1`, contextcap.HashLinkToken(token)).Scan(&extra); err != nil || extra != dmKey {
		t.Fatalf("dm link extra_scene_key=%q err=%v", extra, err)
	}

	alice := uuid.NewString()
	w := ctxcapMobile(t, router, http.MethodPost, "/api/context-capabilities/links/redeem", alice, map[string]any{"token": token})
	ctxcapExpectStatus(t, w, http.StatusOK, "dm redeem")
	var redeemed map[string]string
	ctxcapDecode(t, w, &redeemed)
	if redeemed["scope_type"] != contextcap.ScopePerson || redeemed["scope_key"] != ctxcapStaff {
		t.Fatalf("redeem=%v", redeemed)
	}
	sceneGrant, err := contextcap.GetLiveGrant(ctx, testPool, alice, agentID, contextcap.ScopeScene, ctxcapOrg, dmKey)
	if err != nil || sceneGrant.ScopeTitle != "Alice" {
		t.Fatalf("dm scene grant=%+v err=%v", sceneGrant, err)
	}
	ctxcapExpiresWithin(t, sceneGrant.ExpiresAt.UTC().Format(time.RFC3339), contextcap.GrantTTLPerson)

	// The configure page lists the DM as a dm scene next to the person scope.
	w = ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID, alice, nil)
	var detail ctxcapAgentDetail
	ctxcapDecode(t, w, &detail)
	if detail.Person == nil || len(detail.Scenes) != 1 || detail.Scenes[0].ScopeKey != dmKey || detail.Scenes[0].Kind != "dm" {
		t.Fatalf("agent detail person=%+v scenes=%+v", detail.Person, detail.Scenes)
	}
	w = ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, dmKey), alice, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "dm scene detail")
	var scene ctxcapSceneDetail
	ctxcapDecode(t, w, &scene)
	// A 1:1 chat's configuration is its person's: the link's staffId.
	if scene.Scene.Kind != "dm" || scene.Scene.ScopeKey != dmKey || scene.Scope == nil ||
		*scene.Scope != (contextCapScopeRef{Type: contextcap.ScopePerson, Key: ctxcapStaff, Title: "Alice"}) || !scene.CanConnect ||
		!ctxcapHasBinding(scene.Bindings, f.person, true) {
		t.Fatalf("dm scene=%+v", scene)
	}

	// The person turns a connector on in the DM: it lands in the person
	// scope; admins see the DM scene with that configuration.
	dmOnly := f.insertConnector(t, "none", "")
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM internal_connector WHERE id = $1`, dmOnly)
	})
	f.offer(t, f.scene, f.person, dmOnly)
	w = ctxcapMobile(t, router, http.MethodPut, "/api/context-capabilities/agents/"+agentID+"/bindings", alice, map[string]any{
		"scope_type": "scene", "scope_key": dmKey, "resource_type": "connector", "resource_id": dmOnly, "enabled": true,
	})
	ctxcapExpectStatus(t, w, http.StatusOK, "dm scene binding")
	var bindingResp struct {
		Binding contextCapBindingDTO `json:"binding"`
	}
	ctxcapDecode(t, w, &bindingResp)
	if bindingResp.Binding.ShareInGroups == nil || *bindingResp.Binding.ShareInGroups {
		t.Fatalf("dm binding is not a personal connector binding: %+v", bindingResp.Binding)
	}
	if stored, err := contextcap.ListScopeBindings(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopeScene, ctxcapOrg, dmKey); err != nil || len(stored) != 0 {
		t.Fatalf("dm scene scope rows=%+v err=%v", stored, err)
	}
	dmNode := ctxNode(t, router, "", agentID, contextcap.ScopeScene, dmKey)
	if dm := dmNode.Scene; dm == nil || dm.Kind != "dm" || dm.Title != "Alice" {
		t.Fatalf("admin dm scene=%+v", dm)
	}
	adminBindings := ctxNodeEnabled(dmNode)
	if dmNode.Scope == nil || dmNode.Scope.Type != contextcap.ScopePerson || dmNode.Scope.Key != ctxcapStaff ||
		len(adminBindings) != 2 || !adminBindings[dmOnly] || !adminBindings[f.person] {
		t.Fatalf("admin dm node scope=%+v switches=%+v", dmNode.Scope, adminBindings)
	}
	// The person node is the same configuration, with the DM as its scene.
	personNode := ctxNode(t, router, "", agentID, contextcap.ScopePerson, ctxcapStaff)
	if personNode.Scene == nil || personNode.Scene.SceneKey != dmKey || len(ctxNodeEnabled(personNode)) != 2 || personNode.CanConnect {
		t.Fatalf("person node scene=%+v switches=%v can_connect=%v", personNode.Scene, ctxNodeEnabled(personNode), personNode.CanConnect)
	}

	// A DM's configuration is the person's, and the person's layer already
	// applies to the DM's runs.
	if _, mounted := f.resolve(t, dmTask)[dmOnly]; !mounted {
		t.Fatal("the DM's configuration was not mounted through the person layer")
	}
}
