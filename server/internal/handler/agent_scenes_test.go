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
	"github.com/multica-ai/multica/server/internal/scene"
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
// sceneMemoryRow gives a scene Scene Memory, registering the scene first
// when key is a conversation id rather than a fixture scene id, and returns
// the scene id.
func (f *ctxcapFixture) sceneMemoryRow(t *testing.T, orgID, key, kind, title string, age time.Duration) string {
	t.Helper()
	sceneID := key
	if _, fixture := ctxcapSceneCIDs[key]; !fixture {
		sceneID = f.sceneFor(t, orgID, kind, key, title, age)
	}
	if _, err := testPool.Exec(context.Background(), `INSERT INTO agent_scene_memory
		(scene_id, workspace_id, agent_id, memory_text, updated_at)
		VALUES ($1, $2, $3, 'notes', now() - make_interval(secs => $4::double precision))`,
		sceneID, testWorkspaceID, uuidToString(f.agent), age.Seconds()); err != nil {
		t.Fatal(err)
	}
	return sceneID
}

// sceneFor registers (or finds) the agent's scene of conversation cid in
// orgID, active age ago, and returns its scene id.
func (f *ctxcapFixture) sceneFor(t *testing.T, orgID, kind, cid, title string, age time.Duration) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(), `INSERT INTO agent_scene
		(workspace_id, agent_id, provider, tenant_org_id, source_namespace, scene_kind, external_scene_id, title, last_active_at)
		VALUES ($1, $2, 'dingtalk', $3, 'dingtalk.open_conversation_id', $4, $5, $6, now() - make_interval(secs => $7::double precision))
		ON CONFLICT (workspace_id, agent_id, provider, tenant_org_id, source_namespace, external_scene_id)
		DO UPDATE SET last_active_at = GREATEST(agent_scene.last_active_at, EXCLUDED.last_active_at)
		RETURNING id::text`, testWorkspaceID, uuidToString(f.agent), orgID, kind, cid, title, age.Seconds()).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
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
	// The dispatch registered (or found) the conversation's scene, active
	// when the job ran, and carries its SceneRef. A job of an unknown
	// conversation type or without an org has no scene.
	if kind, known := scene.KindFromConversationType(conversationType); known && dispatchOrg != "" {
		owner := scene.Owner{WorkspaceID: parseUUID(testWorkspaceID), AgentID: f.agent}
		sc, err := scene.Resolve(ctx, f.h.Queries, owner, scene.DingTalkConversation(dispatchOrg, kind, cid),
			scene.Observation{Title: title, ActiveAt: time.Now().Add(-age)})
		if err != nil {
			t.Fatalf("register scene %s: %v", cid, err)
		}
		command["agent_scene"] = map[string]any{"scene_id": uuidToString(sc.ID)}
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
			`DELETE FROM agent_scene_memory WHERE agent_id = $1`,
			`DELETE FROM agent_scene WHERE agent_id = $1`,
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

// The scene list is the agent's scene directory in the tenant org: groups
// and 1:1 chats alike, newest activity first, with memory and the inbound
// session joined by scene_id (docs/agent-scene.md).
func TestAgentScenesListReadsTheSceneDirectory(t *testing.T) {
	f := newCtxcapFixture(t)
	f.cleanupScenes(t)
	router := scenesRouter(f.h)
	agentID := uuidToString(f.agent)
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_scene SET last_active_at = now() - interval '5 hours' WHERE agent_id = $1`, agentID); err != nil {
		t.Fatal(err)
	}

	memoryScene := f.sceneMemoryRow(t, ctxcapOrg, scenesMemoryKey, "group", "Memory group", 3*time.Hour)
	memorySession := f.coordinatorJob(t, scenesMemoryKey, "group", "Memory group", "Bob", ctxcapOrg, 30*time.Minute)
	directScene := f.sceneFor(t, ctxcapOrg, "dm", scenesDirectKey, "Alice", time.Hour)
	f.coordinatorJob(t, scenesDirectKey, "single", "", "Alice", ctxcapOrg, 2*time.Hour)
	directSession := f.coordinatorJob(t, scenesDirectKey, "single", "", "Alice", ctxcapOrg, time.Hour)
	// A scene of another org is not a scene of this tenant.
	otherScene := f.sceneMemoryRow(t, "org-other", scenesStaleKey, "group", "Old org group", time.Minute)

	list := tenantGroups(t, router, agentID, "")
	if list.HasMore || len(list.Scenes) != 4 {
		t.Fatalf("scenes=%+v has_more=%v", list.Scenes, list.HasMore)
	}
	if list.Scenes[0].SceneID != memoryScene || list.Scenes[1].SceneID != directScene {
		t.Fatalf("scenes not ordered by last activity: %+v", list.Scenes)
	}
	byKey := scenesByKey(list.Scenes)
	memory := byKey[memoryScene]
	if memory.Kind != "group" || memory.Title != "Memory group" || memory.SceneKey != memoryScene || memory.ConversationID != scenesMemoryKey ||
		memory.MemoryID != memoryScene || !memory.HasMemory || memory.InboundSessionID != memorySession || memory.InboundCount != 1 {
		t.Fatalf("memory scene=%+v", memory)
	}
	direct := byKey[directScene]
	if direct.Kind != "dm" || direct.Title != "Alice" || direct.InboundSessionID != directSession || direct.InboundCount != 2 || direct.HasMemory {
		t.Fatalf("direct scene=%+v", direct)
	}
	if fixture := byKey[ctxcapScene]; fixture.Kind != "group" || fixture.ConversationID != ctxcapSceneCID {
		t.Fatalf("fixture scene=%+v", fixture)
	}
	groups := tenantGroups(t, router, agentID, "groups_only=true")
	if len(groups.Scenes) != 3 || scenesByKey(groups.Scenes)[directScene].SceneID != "" {
		t.Fatalf("groups only=%+v", groups.Scenes)
	}
	// A 1:1 chat's node is its own scene configuration.
	directNode := ctxNode(t, router, "", agentID, contextcap.ScopeScene, directScene)
	if directNode.Scene == nil || directNode.Scene.Kind != "dm" || directNode.Scope == nil || directNode.Scope.Key != directScene {
		t.Fatalf("direct node=%+v", directNode)
	}
	// Another org's scene, a conversation id and an unknown id are not nodes
	// of this tenant.
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodGet, ctxNodePath(agentID, ctxcapOrg, contextcap.ScopeScene, otherScene), nil),
		http.StatusNotFound, "scene of another org")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodGet, ctxNodePath(agentID, ctxcapOrg, contextcap.ScopeScene, uuid.NewString()), nil),
		http.StatusNotFound, "unknown scene")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodGet, ctxNodePath(agentID, ctxcapOrg, contextcap.ScopeScene, scenesMemoryKey), nil),
		http.StatusBadRequest, "conversation id as a scene key")

	// Pagination.
	list = tenantGroups(t, router, agentID, "limit=1")
	if !list.HasMore || len(list.Scenes) != 1 || list.Scenes[0].SceneID != memoryScene {
		t.Fatalf("first page=%+v has_more=%v", list.Scenes, list.HasMore)
	}
	list = tenantGroups(t, router, agentID, "limit=1&offset=1")
	if !list.HasMore || len(list.Scenes) != 1 || list.Scenes[0].SceneID != directScene {
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
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, ctxNodePath(agentID, ctxcapOrg, contextcap.ScopeScene, ctxcapUnknownScene)+"/prompts", map[string]any{"prompts": []map[string]any{}}), http.StatusNotFound, "unknown scene")
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
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, ctxNodePath(agentID, ctxcapOrg, contextcap.ScopeScene, ctxcapUnknownScene)+"/bindings", map[string]any{"resource_type": "connector", "resource_id": f.person, "enabled": true}), http.StatusNotFound, "unknown scene binding")
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

// The scene detail opens its memory by scene id, whatever its position in
// the capped agent-wide list; there is no separate memory id.
func TestAgentSceneMemoryBySceneID(t *testing.T) {
	f := newCtxcapFixture(t)
	f.cleanupScenes(t)
	f.h.SceneMemoryStore = scenememory.NewStore(f.h.Queries)
	router := chi.NewRouter()
	router.Get("/api/agents/{id}/scene-memory/{sceneId}", f.h.GetAgentSceneMemory)
	agentID := uuidToString(f.agent)
	const key = "cidScenesMemoryByID=="
	sceneID := f.sceneMemoryRow(t, "org-previous", key, "group", "Old group", 400*time.Hour)
	w := scenesAs(t, router, "", http.MethodGet, "/api/agents/"+agentID+"/scene-memory/"+sceneID, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "memory by scene id")
	var memory sceneMemoryResponse
	ctxcapDecode(t, w, &memory)
	if memory.ID != sceneID || memory.SceneID != sceneID || memory.SceneKey != sceneID || memory.ConversationID != key || memory.OrgID != "org-previous" || memory.MemoryText != "notes" {
		t.Fatalf("memory=%+v", memory)
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodGet, "/api/agents/"+agentID+"/scene-memory/"+uuid.NewString(), nil), http.StatusNotFound, "unknown scene")
	// Another agent's scene id does not open this agent's memory.
	other := createHandlerTestAgent(t, "scene-memory-other-"+uuid.NewString()[:8], nil)
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodGet, "/api/agents/"+other+"/scene-memory/"+sceneID, nil), http.StatusNotFound, "another agent's scene")
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
	// A group takes no share_in_groups: its link holder and a manager (who
	// both may toggle the group) get the 400.
	sceneShare := map[string]any{"scope_type": "scene", "scope_key": ctxcapScene, "resource_type": "connector", "resource_id": f.scene, "enabled": true, "share_in_groups": true}
	ctxcapExpectStatus(t, put(sceneShare), http.StatusBadRequest, "share_in_groups on a scene by its link holder")
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

// The link minted in a 1:1 chat is that chat's scene link, keyed by its
// scene_id exactly like a group's, and grants the chat's scene, never a
// person scope. The chat's configuration is the scene's; the person's own
// configuration stays the person scope, and both layers apply to the chat's
// runs (docs/agent-scene.md).
func TestContextCapabilitiesDirectLinkGrantsDMScene(t *testing.T) {
	f := newCtxcapFixture(t)
	f.h.cfg.AppURL = "https://app.multica.example"
	f.cleanupGrantsAndLinks(t)
	f.cleanupScenes(t)
	router := scenesRouter(f.h)
	agentID := uuidToString(f.agent)
	const dmCID = "cidCtxcapDirect=="
	dmScene := f.sceneFor(t, ctxcapOrg, "dm", dmCID, "Alice", time.Minute)
	ctx := context.Background()

	// A group gets its scene link; a DM dispatch without a scene has no
	// conversation to configure.
	groupLink, isError, text := ctxcapToolResult(t, f.ctxcapToolCall(t, f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff)), nil))
	if isError || groupLink.Scope != contextcap.ScopeScene || groupLink.SceneKind != contextcap.SceneKindGroup {
		t.Fatalf("group mint isError=%v text=%q", isError, text)
	}
	if _, isError, text := ctxcapToolResult(t, f.ctxcapToolCall(t, f.task(t, ctxcapDispatch("single", "", ctxcapStaff, ctxcapStaff)), nil)); !isError {
		t.Fatalf("dm mint without a scene: %s", text)
	}

	dmContext, _ := json.Marshal(map[string]any{
		"agent_scene":     map[string]any{"scene_id": dmScene},
		"dispatch_source": map[string]any{"platform": "dingtalk"},
		"dispatch_event_data": map[string]any{
			"conversation": map[string]any{"openConversationId": dmCID, "type": "single"},
			"sender":       map[string]any{"staffId": ctxcapStaff, "displayName": "Alice"},
			"messages":     []map[string]any{{"openMsgId": "ma", "senderStaffId": ctxcapStaff}},
		},
	})
	// The DM's link is its scene link, minted like a group's: keyed by the
	// DM scene (its conversation), not by the person.
	dmTask := f.task(t, dmContext)
	dmLink, isError, text := ctxcapToolResult(t, f.ctxcapToolCall(t, dmTask, nil))
	if isError || dmLink.Scope != contextcap.ScopeScene || dmLink.SceneKind != contextcap.SceneKindDM {
		t.Fatalf("dm mint isError=%v text=%q", isError, text)
	}
	token := ctxcapLinkToken(t, dmLink)
	var scopeKey, extra string
	if err := testPool.QueryRow(ctx, `SELECT scope_key, extra_scene_key FROM context_config_link WHERE token_hash = $1`, contextcap.HashLinkToken(token)).Scan(&scopeKey, &extra); err != nil || scopeKey != dmScene || extra != "" {
		t.Fatalf("dm link scope_key=%q extra_scene_key=%q err=%v", scopeKey, extra, err)
	}

	alice := uuid.NewString()
	w := ctxcapMobile(t, router, http.MethodPost, "/api/context-capabilities/links/redeem", alice, map[string]any{"token": token})
	ctxcapExpectStatus(t, w, http.StatusOK, "dm redeem")
	var redeemed map[string]string
	ctxcapDecode(t, w, &redeemed)
	if redeemed["scope_type"] != contextcap.ScopeScene || redeemed["scope_key"] != dmScene || redeemed["scope_title"] != "Alice" {
		t.Fatalf("redeem=%v", redeemed)
	}
	sceneGrant, err := contextcap.GetLiveGrant(ctx, testPool, alice, agentID, contextcap.ScopeScene, ctxcapOrg, dmScene)
	if err != nil || sceneGrant.ScopeTitle != "Alice" {
		t.Fatalf("dm scene grant=%+v err=%v", sceneGrant, err)
	}
	ctxcapExpiresWithin(t, sceneGrant.ExpiresAt.UTC().Format(time.RFC3339), contextcap.GrantTTLScene)

	// The configure page lists the DM as a dm scene; the link grants no
	// personal scope.
	w = ctxcapMobile(t, router, http.MethodGet, "/api/context-capabilities/agents/"+agentID, alice, nil)
	var detail ctxcapAgentDetail
	ctxcapDecode(t, w, &detail)
	if detail.Person != nil || len(detail.Scenes) != 1 || detail.Scenes[0].ScopeKey != dmScene || detail.Scenes[0].Kind != "dm" {
		t.Fatalf("agent detail person=%+v scenes=%+v", detail.Person, detail.Scenes)
	}
	// The DM scene is its own scope: the link holder reads and changes it
	// like a manager.
	w = ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, dmScene), alice, nil)
	ctxcapExpectStatus(t, w, http.StatusOK, "dm scene detail")
	var scene ctxcapSceneDetail
	ctxcapDecode(t, w, &scene)
	if scene.Scene.Kind != "dm" || scene.Scene.ScopeKey != dmScene || scene.Scope == nil ||
		*scene.Scope != (contextCapScopeRef{Type: contextcap.ScopeScene, Key: dmScene, Title: "Alice"}) || !scene.CanConnect || scene.Rights != contextCapSceneRights {
		t.Fatalf("dm scene=%+v", scene)
	}
	dmOnly := f.insertConnector(t, "none", "")
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM internal_connector WHERE id = $1`, dmOnly)
	})
	f.offer(t, f.scene, f.person, dmOnly)
	w = ctxcapMobile(t, router, http.MethodPut, "/api/context-capabilities/agents/"+agentID+"/bindings", alice, map[string]any{
		"scope_type": "scene", "scope_key": dmScene, "resource_type": "connector", "resource_id": dmOnly, "enabled": true,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("link holder write to the dm scene: %d %s", w.Code, w.Body.String())
	}
	// A manager configures the DM scene; the person configures herself.
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, ctxNodePath(agentID, ctxcapOrg, contextcap.ScopeScene, dmScene)+"/bindings",
		map[string]any{"resource_type": "connector", "resource_id": dmOnly, "enabled": true}), http.StatusOK, "manager dm binding")
	if stored, err := contextcap.ListScopeBindings(ctx, testPool, testWorkspaceID, agentID, contextcap.ScopeScene, ctxcapOrg, dmScene); err != nil || len(stored) != 1 {
		t.Fatalf("dm scene scope rows=%+v err=%v", stored, err)
	}
	dmNode := ctxNode(t, router, "", agentID, contextcap.ScopeScene, dmScene)
	if dmNode.Scene == nil || dmNode.Scene.Kind != "dm" || dmNode.Scope == nil || dmNode.Scope.Type != contextcap.ScopeScene || !ctxNodeEnabled(dmNode)[dmOnly] {
		t.Fatalf("admin dm node=%+v", dmNode)
	}
	// The DM's runs get the DM scene layer and the person's layer.
	resolved := f.resolve(t, dmTask)
	if got, mounted := resolved[dmOnly]; !mounted || got.binding != contextcap.LayerScene {
		t.Fatalf("the DM scene's connector was not mounted from the scene layer: %+v", resolved)
	}
	if got, mounted := resolved[f.person]; !mounted || got.binding != contextcap.LayerPerson {
		t.Fatalf("the person's connector was not mounted from the person layer: %+v", resolved)
	}
}
