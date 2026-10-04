package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	"github.com/multica-ai/multica/server/internal/service/scenememory"
)

func TestEmployeeMemoryManagementLoopScopeAndRevision(t *testing.T) {
	f := newCtxcapFixture(t)
	ctx := context.Background()
	f.h.EmployeeMemory = employeememory.NewStore(testPool)
	f.h.SceneMemoryStore = scenememory.NewStore(f.h.Queries)
	agentID := uuidToString(f.agent)
	sceneID := f.sceneMemoryRow(t, ctxcapOrg, "memory-loop-"+uuid.NewString(), "group", "Memory group", 0)
	if _, err := testPool.Exec(ctx, "UPDATE agent SET coordination_mode='employee' WHERE id=$1", f.agent); err != nil {
		t.Fatal(err)
	}
	scope := employeememory.Scope{WorkspaceID: f.ws, AgentID: f.agent, TenantOrgID: ctxcapOrg, Scene: scene.Ref{SceneID: sceneID}, Kind: employeememory.ScopeScene}
	rec := employeememory.LearningRecord{Type: employeememory.LearningTypePreference, Key: "shared", Insight: "Employee shared knowledge", Confidence: 8}
	ev := employeememory.TrustedEvidence{SourceID: "source-1", EvidenceID: "evidence-1", ActorID: testUserID, HumanStated: true}
	if _, err := f.h.EmployeeMemory.Record(ctx, scope, rec, ev); err != nil {
		t.Fatal(err)
	}
	private := scope
	private.Kind = employeememory.ScopePrivate
	private.PrincipalID = "another-person"
	rec.Key = "private"
	rec.Insight = "PRIVATE_OTHER_PRINCIPAL"
	ev.SourceID = "private"
	ev.EvidenceID = "private"
	if _, err := f.h.EmployeeMemory.Record(ctx, private, rec, ev); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), "DELETE FROM employee_learning WHERE agent_id=$1", f.agent)
		_, _ = testPool.Exec(context.Background(), "DELETE FROM employee_memory_state WHERE agent_id=$1", f.agent)
	})
	router := chi.NewRouter()
	router.Get("/api/agents/{id}/scene-memory", f.h.ListAgentSceneMemory)
	router.Get("/api/agents/{id}/scene-memory/{sceneId}", f.h.GetAgentSceneMemory)
	router.Put("/api/agents/{id}/scene-memory/{sceneId}", f.h.UpdateAgentSceneMemory)
	router.Post("/api/agents/{id}/scene-memory/{sceneId}/reset", f.h.ResetAgentSceneMemory)
	base := "/api/agents/" + agentID + "/scene-memory"
	detail := base + "/" + sceneID
	response := scenesAs(t, router, "", http.MethodGet, detail+"?loop=employee", nil)
	ctxcapExpectStatus(t, response, http.StatusOK, "employee detail")
	if !strings.Contains(response.Body.String(), "Employee shared knowledge") || strings.Contains(response.Body.String(), "PRIVATE_OTHER_PRINCIPAL") || strings.Contains(response.Body.String(), `"memory_text":"notes"`) {
		t.Fatalf("memory boundary failed: %s", response.Body.String())
	}
	response = scenesAs(t, router, "", http.MethodGet, base+"?loop=employee", nil)
	ctxcapExpectStatus(t, response, http.StatusOK, "employee list")
	if !strings.Contains(response.Body.String(), sceneID) || strings.Contains(response.Body.String(), "PRIVATE_OTHER_PRINCIPAL") {
		t.Fatal("list leaked principal or omitted scene")
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodGet, detail+"?loop=invalid", nil), http.StatusBadRequest, "unknown loop")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, detail+"?loop=employee", map[string]any{"memory_text": "overwrite", "expected_revision": 1, "scene_id": sceneID, "org_id": ctxcapOrg}), http.StatusMethodNotAllowed, "employee is learning-backed")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPost, detail+"/reset", nil), http.StatusConflict, "old page cannot reset coordinator while employee selected")
	body := map[string]any{"scene_id": sceneID, "org_id": ctxcapOrg, "expected_revision": 0}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPost, detail+"/reset?loop=employee", body), http.StatusConflict, "stale reset")
	body["expected_revision"] = 1
	body["org_id"] = "other-org"
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPost, detail+"/reset?loop=employee", body), http.StatusConflict, "stale scope")
	body["org_id"] = ctxcapOrg
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPost, detail+"/reset?loop=employee", body), http.StatusOK, "employee reset")
	var old string
	if err := testPool.QueryRow(ctx, "SELECT memory_text FROM agent_scene_memory WHERE scene_id=$1", sceneID).Scan(&old); err != nil || old != "notes" {
		t.Fatalf("coordinator altered: %q %v", old, err)
	}
	if _, err := testPool.Exec(ctx, "UPDATE agent SET coordination_mode='coordinator' WHERE id=$1", f.agent); err != nil {
		t.Fatal(err)
	}
	body["expected_revision"] = 2
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPost, detail+"/reset?loop=employee", body), http.StatusConflict, "switched loop rejects delayed reset")
	response = scenesAs(t, router, "", http.MethodGet, detail, nil)
	ctxcapExpectStatus(t, response, http.StatusOK, "legacy coordinator read")
	if !strings.Contains(response.Body.String(), `"memory_text":"notes"`) {
		t.Fatal("legacy read changed namespace")
	}

	coordinator := map[string]any{"loop": "coordinator", "scene_id": sceneID, "org_id": ctxcapOrg, "expected_revision": 0, "memory_text": "Coordinator revised"}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPut, detail+"?loop=coordinator", coordinator), http.StatusOK, "explicit Coordinator update")
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPost, detail+"/reset?loop=coordinator", coordinator), http.StatusConflict, "Coordinator reset revision")
	coordinator["expected_revision"] = 1
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodPost, detail+"/reset?loop=coordinator", coordinator), http.StatusOK, "explicit Coordinator reset")
	employeeAfter, err := f.h.EmployeeMemory.ManagedScene(ctx, scope)
	if err != nil || employeeAfter.Revision != 2 || len(employeeAfter.Learnings) != 0 {
		t.Fatalf("Coordinator write crossed namespaces: %+v err=%v", employeeAfter, err)
	}
	if _, err := testPool.Exec(ctx, "UPDATE agent SET coordination_mode='employee' WHERE id=$1", f.agent); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, "UPDATE agent_dingtalk_identity SET org_id='new-org' WHERE agent_id=$1", f.agent); err != nil {
		t.Fatal(err)
	}
	ctxcapExpectStatus(t, scenesAs(t, router, "", http.MethodGet, detail+"?loop=employee", nil), http.StatusNotFound, "old tenant rejected")
}
