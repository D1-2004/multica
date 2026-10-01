package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/tag"
)

func tagTestRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.RequireWorkspaceMember(h.Queries))
		r.Route("/api/tag", func(r chi.Router) {
			r.Get("/", h.GetTag)
			r.With(RequireHumanActor).Post("/", h.CreateTag)
			r.With(RequireHumanActor).Patch("/", h.UpdateTag)
			r.With(RequireHumanActor).Delete("/", h.DeleteTag)
			r.With(RequireHumanActor).Post("/tenants", h.CreateTagTenant)
			r.With(RequireHumanActor).Post("/tenants/adopt", h.AdoptTagTenant)
			r.With(RequireHumanActor).Patch("/tenants/{tenantId}", h.RenameTagTenant)
			r.With(RequireHumanActor).Delete("/tenants/{tenantId}", h.DeleteTagTenant)
			r.With(RequireHumanActor).Post("/apply", h.ApplyTag)
		})
		r.Post("/api/agents/{id}/archive", h.ArchiveAgent)
		r.Put("/api/agents/{id}", h.UpdateAgent)
		r.With(h.RefuseTagEmployeeConfigWrites).Put("/api/agents/{id}/skills", h.SetAgentSkills)
		r.With(h.RefuseTagEmployeeConfigWrites).Put("/api/agents/{id}/env", h.UpdateAgentEnv)
		r.With(RequireHumanActor, h.RefuseTagEmployeeConfigWrites).Put("/api/agents/{id}/context-capabilities/offers", h.PutAgentContextCapabilityOffers)
		r.With(RequireHumanActor).Post("/api/agents/{id}/tenants", h.CreateAgentTenant)
		r.With(RequireHumanActor).Patch("/api/agents/{id}/tenants/{orgId}", h.RenameAgentTenant)
		r.Put("/api/agents/{id}/a2a/operator/dws-identity", h.UpdateAgentA2AOperatorIdentity)
		r.With(RequireHumanActor).Post("/api/workspaces/{id}/dingtalk/execution-identities/reuse", h.ReuseDingTalkIdentity)
	})
	return r
}

func tagDo(t *testing.T, router http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, newRequest(method, path, body))
	return w
}

func tagExpect(t *testing.T, w *httptest.ResponseRecorder, status int, what string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("%s: status %d, want %d: %s", what, w.Code, status, w.Body.String())
	}
}

func tagDecode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return out
}

// cleanupTag removes every Tag row and agent the test created in the shared
// handler-test workspace.
func cleanupTag(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		for _, stmt := range []string{
			`DELETE FROM agent_skill WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1 AND (name LIKE 'TagTest%' OR name = 'Tag' OR name LIKE 'Tag · %'))`,
			`DELETE FROM agent_invocation_target WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1 AND (name LIKE 'TagTest%' OR name = 'Tag' OR name LIKE 'Tag · %'))`,
			`DELETE FROM tag_config_revision WHERE workspace_id = $1`,
			`DELETE FROM tag_tenant WHERE workspace_id = $1`,
			`DELETE FROM workspace_tag WHERE workspace_id = $1`,
			`DELETE FROM agent WHERE workspace_id = $1 AND (name LIKE 'TagTest%' OR name = 'Tag' OR name LIKE 'Tag · %')`,
		} {
			if _, err := testPool.Exec(ctx, stmt, testWorkspaceID); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
	})
}

func useTagOperator(t *testing.T, operator bool) {
	t.Helper()
	emails := []string{"someone-else@multica.test"}
	if operator {
		emails = []string{handlerTestEmail}
	}
	configureAgentA2AOperatorTestHandler(t, Config{A2AOperatorEmails: emails})
}

func TestTagCreateIsOperatorOnlyAndCloudOnly(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	cleanupTag(t)
	router := tagTestRouter(testHandler)
	useTagOperator(t, false)
	state := tagDecode[TagStateResponse](t, tagDo(t, router, http.MethodGet, "/api/tag", nil))
	if state.Tag != nil || state.CanOperate {
		t.Fatalf("state for non-operator = %+v", state)
	}
	tagExpect(t, tagDo(t, router, http.MethodPost, "/api/tag", map[string]string{"runtime_id": testRuntimeID}),
		http.StatusForbidden, "non-operator create")

	useTagOperator(t, true)
	var localRuntime string
	if err := testPool.QueryRow(context.Background(), `INSERT INTO agent_runtime
		(workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, owner_id, last_seen_at)
		VALUES ($1, NULL, 'TagTest local runtime', 'local', 'handler_test_runtime', 'online', 'local', '{}'::jsonb, $2, now())
		RETURNING id`, testWorkspaceID, testUserID).Scan(&localRuntime); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, localRuntime) })
	tagExpect(t, tagDo(t, router, http.MethodPost, "/api/tag", map[string]string{"runtime_id": localRuntime}),
		http.StatusBadRequest, "local runtime")

	w := tagDo(t, router, http.MethodPost, "/api/tag", map[string]string{"runtime_id": testRuntimeID, "description": "多企业数字员工"})
	tagExpect(t, w, http.StatusCreated, "operator create")
	created := tagDecode[TagStateResponse](t, w)
	if created.Tag == nil || created.Tag.Name != "Tag" || created.Tag.LatestRevision == nil || *created.Tag.LatestRevision != 1 ||
		!created.Tag.SidebarVisible || created.Tag.HasUnpublishedChanges || !created.CanOperate || !created.CanManage {
		t.Fatalf("created state = %+v", created)
	}
	tagExpect(t, tagDo(t, router, http.MethodPost, "/api/tag", map[string]string{"runtime_id": testRuntimeID}),
		http.StatusConflict, "second tag")

	// Sidebar visibility is an operator switch too.
	useTagOperator(t, false)
	tagExpect(t, tagDo(t, router, http.MethodPatch, "/api/tag", map[string]bool{"sidebar_visible": false}), http.StatusForbidden, "non-operator hide")
	useTagOperator(t, true)
	hidden := tagDecode[TagStateResponse](t, tagDo(t, router, http.MethodPatch, "/api/tag", map[string]bool{"sidebar_visible": false}))
	if hidden.Tag == nil || hidden.Tag.SidebarVisible {
		t.Fatalf("hidden state = %+v", hidden)
	}

	// The template cannot be archived while it is the Tag, and cannot take
	// extra contextcap tenants.
	templatePath := "/api/agents/" + created.Tag.AgentID
	tagExpect(t, tagDo(t, router, http.MethodPost, templatePath+"/archive", nil), http.StatusConflict, "archive template")
	tagExpect(t, tagDo(t, router, http.MethodPost, templatePath+"/tenants", map[string]string{"org_id": "org-x", "name": "X"}),
		http.StatusConflict, "contextcap tenant on template")

	tagExpect(t, tagDo(t, router, http.MethodDelete, "/api/tag", nil), http.StatusNoContent, "remove tag")
	if gone := tagDecode[TagStateResponse](t, tagDo(t, router, http.MethodGet, "/api/tag", nil)); gone.Tag != nil {
		t.Fatalf("tag after delete = %+v", gone.Tag)
	}
}

func TestTagTenantsAndApply(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	cleanupTag(t)
	useTagOperator(t, true)
	router := tagTestRouter(testHandler)
	ctx := context.Background()
	created := tagDecode[TagStateResponse](t, tagDo(t, router, http.MethodPost, "/api/tag", map[string]string{"runtime_id": testRuntimeID}))
	templateID := created.Tag.AgentID
	if _, err := testPool.Exec(ctx, `UPDATE agent SET instructions = 'v1 instructions', persona = 'calm' WHERE id = $1`, templateID); err != nil {
		t.Fatal(err)
	}

	// A new tenant gets its own employee agent with the latest config.
	w := tagDo(t, router, http.MethodPost, "/api/tag/tenants", map[string]string{"name": "Think测试组织"})
	tagExpect(t, w, http.StatusCreated, "create tenant")
	tenant := tagDecode[TagTenantMutationResponse](t, w)
	if tenant.Tenant.EmployeeName != "Tag · Think测试组织" || tenant.Tenant.Bound || tenant.Tenant.AppliedRevision == nil || *tenant.Tenant.AppliedRevision != 2 ||
		tenant.Apply == nil || !tenant.Apply.Applied {
		t.Fatalf("tenant = %+v apply=%+v", tenant.Tenant, tenant.Apply)
	}
	var instructions, persona, permission string
	if err := testPool.QueryRow(ctx, `SELECT instructions, persona, permission_mode FROM agent WHERE id = $1`, tenant.Tenant.EmployeeAgentID).
		Scan(&instructions, &persona, &permission); err != nil {
		t.Fatal(err)
	}
	if instructions != "v1 instructions" || persona != "calm" || permission != "public_to" {
		t.Fatalf("employee config = %q %q %q", instructions, persona, permission)
	}
	employeePath := "/api/agents/" + tenant.Tenant.EmployeeAgentID
	tagExpect(t, tagDo(t, router, http.MethodPost, employeePath+"/tenants", map[string]string{"org_id": "org-x", "name": "X"}),
		http.StatusConflict, "contextcap tenant on employee")

	// Template edits are pending until applied.
	if _, err := testPool.Exec(ctx, `UPDATE agent SET instructions = 'v2 instructions' WHERE id = $1`, templateID); err != nil {
		t.Fatal(err)
	}
	pending := tagDecode[TagStateResponse](t, tagDo(t, router, http.MethodGet, "/api/tag", nil))
	if pending.Tag == nil || !pending.Tag.HasUnpublishedChanges || len(pending.Tenants) != 1 {
		t.Fatalf("pending state = %+v", pending)
	}
	if err := testPool.QueryRow(ctx, `SELECT instructions FROM agent WHERE id = $1`, tenant.Tenant.EmployeeAgentID).Scan(&instructions); err != nil {
		t.Fatal(err)
	}
	if instructions != "v1 instructions" {
		t.Fatalf("unapplied edit leaked: %q", instructions)
	}
	tagExpect(t, tagDo(t, router, http.MethodPost, "/api/tag/apply", map[string]any{"tenant_ids": []string{}}), http.StatusBadRequest, "empty apply")
	w = tagDo(t, router, http.MethodPost, "/api/tag/apply", map[string]any{"tenant_ids": []string{tenant.Tenant.ID}, "note": "v2"})
	tagExpect(t, w, http.StatusOK, "apply")
	applied := tagDecode[TagApplyResponse](t, w)
	if applied.Revision != 3 || !applied.Published || len(applied.Results) != 1 || !applied.Results[0].Applied {
		t.Fatalf("apply = %+v", applied)
	}
	if err := testPool.QueryRow(ctx, `SELECT instructions FROM agent WHERE id = $1`, tenant.Tenant.EmployeeAgentID).Scan(&instructions); err != nil {
		t.Fatal(err)
	}
	if instructions != "v2 instructions" {
		t.Fatalf("applied instructions = %q", instructions)
	}

	// Tenant-owned settings are neither versioned nor overwritten: a persona
	// edited on the template is not a pending change, and an apply keeps the
	// tenant's own persona.
	if _, err := testPool.Exec(ctx, `UPDATE agent SET persona = 'tenant voice' WHERE id = $1`, tenant.Tenant.EmployeeAgentID); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent SET persona = 'template voice' WHERE id = $1`, templateID); err != nil {
		t.Fatal(err)
	}
	if st := tagDecode[TagStateResponse](t, tagDo(t, router, http.MethodGet, "/api/tag", nil)); st.Tag == nil || st.Tag.HasUnpublishedChanges {
		t.Fatalf("persona edit counted as a shared change: %+v", st.Tag)
	}
	reapplied := tagDecode[TagApplyResponse](t, tagDo(t, router, http.MethodPost, "/api/tag/apply", map[string]any{"tenant_ids": []string{tenant.Tenant.ID}}))
	if reapplied.Published || reapplied.Revision != 3 {
		t.Fatalf("re-apply = %+v", reapplied)
	}
	if err := testPool.QueryRow(ctx, `SELECT persona FROM agent WHERE id = $1`, tenant.Tenant.EmployeeAgentID).Scan(&persona); err != nil {
		t.Fatal(err)
	}
	if persona != "tenant voice" {
		t.Fatalf("apply overwrote the tenant's persona: %q", persona)
	}

	// The Tag is always called Tag; renaming the tenant renames its employee
	// while the employee still carries its derived name.
	tagExpect(t, tagDo(t, router, http.MethodPut, "/api/agents/"+templateID, map[string]string{"name": "QwenTag"}),
		http.StatusConflict, "rename the template")
	w = tagDo(t, router, http.MethodPatch, "/api/tag/tenants/"+tenant.Tenant.ID, map[string]string{"name": "Think 组织"})
	tagExpect(t, w, http.StatusOK, "rename tenant")
	if renamed := tagDecode[TagTenantMutationResponse](t, w); renamed.Tenant.Name != "Think 组织" || renamed.Tenant.EmployeeName != "Tag · Think 组织" {
		t.Fatalf("renamed tenant = %+v", renamed.Tenant)
	}

	// Adopt an existing agent, rename, then detach it again.
	var adoptee string
	if err := testPool.QueryRow(ctx, `INSERT INTO agent (workspace_id, name, runtime_mode, runtime_config, runtime_id, visibility, permission_mode, max_concurrent_tasks, owner_id)
		VALUES ($1, 'TagTest adoptee', 'cloud', '{}'::jsonb, $2, 'workspace', 'public_to', 1, $3) RETURNING id`,
		testWorkspaceID, testRuntimeID, testUserID).Scan(&adoptee); err != nil {
		t.Fatal(err)
	}
	tagExpect(t, tagDo(t, router, http.MethodPost, "/api/tag/tenants/adopt", map[string]string{"agent_id": templateID, "name": "self"}),
		http.StatusConflict, "adopt template")
	// An agent that already serves another org through a contextcap tenant
	// is not one enterprise's employee.
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_tenant (workspace_id, agent_id, org_id, name) VALUES ($1, $2, 'org-other', 'Other')`,
		testWorkspaceID, adoptee); err != nil {
		t.Fatal(err)
	}
	tagExpect(t, tagDo(t, router, http.MethodPost, "/api/tag/tenants/adopt", map[string]string{"agent_id": adoptee, "name": "钉钉"}),
		http.StatusConflict, "adopt multi-org agent")
	if _, err := testPool.Exec(ctx, `DELETE FROM agent_tenant WHERE agent_id = $1`, adoptee); err != nil {
		t.Fatal(err)
	}
	w = tagDo(t, router, http.MethodPost, "/api/tag/tenants/adopt", map[string]string{"agent_id": adoptee, "name": "钉钉"})
	tagExpect(t, w, http.StatusCreated, "adopt")
	adopted := tagDecode[TagTenantMutationResponse](t, w)
	if adopted.Tenant.AppliedRevision != nil {
		t.Fatalf("adopted tenant applied before an apply: %+v", adopted.Tenant)
	}
	renamed := tagDecode[TagTenantMutationResponse](t, tagDo(t, router, http.MethodPatch, "/api/tag/tenants/"+adopted.Tenant.ID, map[string]string{"name": "钉钉 FDE"}))
	if renamed.Tenant.Name != "钉钉 FDE" || renamed.Tenant.EmployeeName != "TagTest adoptee" {
		t.Fatalf("renamed = %+v", renamed.Tenant)
	}
	tagExpect(t, tagDo(t, router, http.MethodDelete, "/api/tag", nil), http.StatusConflict, "remove tag with tenants")
	tagExpect(t, tagDo(t, router, http.MethodDelete, "/api/tag/tenants/"+adopted.Tenant.ID, nil), http.StatusNoContent, "detach adoptee")
	tagExpect(t, tagDo(t, router, http.MethodDelete, "/api/tag/tenants/"+tenant.Tenant.ID, nil), http.StatusNoContent, "detach employee")
	tagExpect(t, tagDo(t, router, http.MethodDelete, "/api/tag", nil), http.StatusNoContent, "remove tag")
}

func TestTagCreateCopyFromKeepsExplicitFields(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	cleanupTag(t)
	useTagOperator(t, true)
	router := tagTestRouter(testHandler)
	ctx := context.Background()

	sourceID := createHandlerTestAgent(t, "TagTest source "+uuid.NewString()[:8], nil)
	if _, err := testPool.Exec(ctx, `UPDATE agent SET description = 'source description', model = 'source-model',
		instructions = 'source instructions' WHERE id = $1`, sourceID); err != nil {
		t.Fatal(err)
	}

	w := tagDo(t, router, http.MethodPost, "/api/tag", map[string]string{
		"runtime_id":  testRuntimeID,
		"description": "typed description", "copy_from_agent_id": sourceID,
	})
	tagExpect(t, w, http.StatusCreated, "create from source")
	created := tagDecode[TagStateResponse](t, w)

	var description, model, instructions, runtimeID string
	if err := testPool.QueryRow(ctx, `SELECT description, COALESCE(model, ''), instructions, runtime_id::text FROM agent WHERE id = $1`,
		created.Tag.AgentID).Scan(&description, &model, &instructions, &runtimeID); err != nil {
		t.Fatal(err)
	}
	// The typed description wins; the empty model and the instructions come
	// from the source; the runtime is the requested one.
	if description != "typed description" || model != "source-model" || instructions != "source instructions" || runtimeID != testRuntimeID {
		t.Fatalf("template = description %q model %q instructions %q runtime %q", description, model, instructions, runtimeID)
	}
}

func TestTagTemplateRefusesEveryIdentityWrite(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	cleanupTag(t)
	useTagOperator(t, true)
	router := tagTestRouter(testHandler)
	ctx := context.Background()

	created := tagDecode[TagStateResponse](t, tagDo(t, router, http.MethodPost, "/api/tag",
		map[string]string{"runtime_id": testRuntimeID}))
	templateID := created.Tag.AgentID

	reuse := tagDo(t, router, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/dingtalk/execution-identities/reuse",
		map[string]string{"agent_id": templateID, "source_agent_id": uuid.NewString()})
	tagExpect(t, reuse, http.StatusBadRequest, "reuse identity on template")
	manual := tagDo(t, router, http.MethodPut, "/api/agents/"+templateID+"/a2a/operator/dws-identity",
		map[string]string{"uid": "7015073760", "org_id": "439446171", "display_name": "Taggg", "organization_name": "钉钉"})
	tagExpect(t, manual, http.StatusBadRequest, "operator identity on template")
	for _, w := range []*httptest.ResponseRecorder{reuse, manual} {
		if !strings.Contains(w.Body.String(), "tag_template_not_bindable") {
			t.Fatalf("identity write refused for another reason: %s", w.Body.String())
		}
	}
	var identities int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_dingtalk_identity WHERE agent_id = $1`, templateID).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if identities != 0 {
		t.Fatalf("template got %d identities", identities)
	}
}

// A contextcap tenant insert and the agent joining the Tag decide under one
// lock: the insert waits for the join and then sees the agent as a tenant.
func TestContextCapTenantWaitsForTagMembership(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	router := tagTestRouter(testHandler)
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "TagTest race "+uuid.NewString()[:8], nil)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM tag_tenant WHERE employee_agent_id = $1`, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_tenant WHERE agent_id = $1`, agentID)
	})

	join, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer join.Rollback(ctx)
	if err := tag.LockAgentTenancy(ctx, join, agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := join.Exec(ctx, `INSERT INTO tag_tenant (workspace_id, tag_agent_id, employee_agent_id, name, created_by) VALUES ($1, $2, $3, 'race', $4)`,
		testWorkspaceID, uuid.NewString(), agentID, testUserID); err != nil {
		t.Fatal(err)
	}

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- tagDo(t, router, http.MethodPost, "/api/agents/"+agentID+"/tenants", map[string]string{"org_id": "org-race", "name": "Race"})
	}()
	select {
	case w := <-done:
		t.Fatalf("contextcap insert did not wait for the tenancy lock: %d %s", w.Code, w.Body.String())
	case <-time.After(300 * time.Millisecond):
	}
	if err := join.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		tagExpect(t, w, http.StatusConflict, "contextcap insert after joining the tag")
		if !strings.Contains(w.Body.String(), agentTenantErrTagManaged) {
			t.Fatalf("refused for another reason: %s", w.Body.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("contextcap insert still blocked after the join committed")
	}
	var extra int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_tenant WHERE agent_id = $1`, agentID).Scan(&extra); err != nil {
		t.Fatal(err)
	}
	if extra != 0 {
		t.Fatalf("agent gained %d contextcap tenants", extra)
	}
}

// A Tag employee keeps exactly one enterprise even when it is rebound: it
// holds no contextcap tenant rows that could outlive its identity org (S4 B5).
func TestTagEmployeeKeepsOneEnterpriseAcrossRebind(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	cleanupTag(t)
	useTagOperator(t, true)
	router := tagTestRouter(testHandler)
	ctx := context.Background()

	tagDecode[TagStateResponse](t, tagDo(t, router, http.MethodPost, "/api/tag",
		map[string]string{"runtime_id": testRuntimeID}))
	agentID := createHandlerTestAgent(t, "TagTest bound "+uuid.NewString()[:8], nil)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_dingtalk_identity WHERE agent_id = $1`, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_tenant WHERE agent_id = $1`, agentID)
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO agent_dingtalk_identity (agent_id, workspace_id, dws_uid, org_id, bound_by)
		VALUES ($1, $2, '1001', 'orgA', $3)`, agentID, testWorkspaceID, testUserID); err != nil {
		t.Fatal(err)
	}
	// Before joining the Tag the agent names its identity org: an alias row.
	tagExpect(t, tagDo(t, router, http.MethodPatch, "/api/agents/"+agentID+"/tenants/orgA", map[string]string{"name": "A 公司"}),
		http.StatusOK, "alias the identity tenant")

	w := tagDo(t, router, http.MethodPost, "/api/tag/tenants/adopt", map[string]string{"agent_id": agentID, "name": "A 公司"})
	tagExpect(t, w, http.StatusCreated, "adopt bound agent")
	if adopted := tagDecode[TagTenantMutationResponse](t, w); !adopted.Tenant.Bound || adopted.Tenant.OrgID != "orgA" {
		t.Fatalf("adopted tenant = %+v", adopted.Tenant)
	}
	var rows int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_tenant WHERE agent_id = $1`, agentID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("employee kept %d contextcap tenant rows", rows)
	}
	// The contextcap rename cannot store a new alias row for the employee.
	w = tagDo(t, router, http.MethodPatch, "/api/agents/"+agentID+"/tenants/orgA", map[string]string{"name": "A again"})
	tagExpect(t, w, http.StatusConflict, "alias on a tag employee")
	if !strings.Contains(w.Body.String(), agentTenantErrTagManaged) {
		t.Fatalf("refused for another reason: %s", w.Body.String())
	}

	// Rebind to another org: the employee serves only the new one.
	if _, err := testPool.Exec(ctx, `UPDATE agent_dingtalk_identity SET org_id = 'orgB' WHERE agent_id = $1`, agentID); err != nil {
		t.Fatal(err)
	}
	tenants, err := contextcap.AgentTenants(ctx, testPool, testWorkspaceID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tenants) != 1 || tenants[0].OrgID != "orgB" {
		t.Fatalf("tenants after rebind = %+v", tenants)
	}
	if _, err := testPool.Exec(ctx, `DELETE FROM tag_tenant WHERE employee_agent_id = $1`, agentID); err != nil {
		t.Fatal(err)
	}
}

// A tenant's employee inherits its configuration from the Tag: it is readable
// but every write path refuses it, while its tenant-owned settings stay
// writable and the template stays fully writable.
func TestTagEmployeeConfigIsReadOnly(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	cleanupTag(t)
	useTagOperator(t, true)
	router := tagTestRouter(testHandler)
	ctx := context.Background()

	created := tagDecode[TagStateResponse](t, tagDo(t, router, http.MethodPost, "/api/tag", map[string]string{"runtime_id": testRuntimeID}))
	templatePath := "/api/agents/" + created.Tag.AgentID
	tenant := tagDecode[TagTenantMutationResponse](t, tagDo(t, router, http.MethodPost, "/api/tag/tenants", map[string]string{"name": "只读"}))
	employeeID := tenant.Tenant.EmployeeAgentID
	employeePath := "/api/agents/" + employeeID

	inherited := []struct {
		what   string
		method string
		path   string
		body   any
	}{
		{"instructions", http.MethodPut, employeePath, map[string]string{"instructions": "x"}},
		{"model", http.MethodPut, employeePath, map[string]string{"model": "m"}},
		{"name", http.MethodPut, employeePath, map[string]string{"name": "Renamed"}},
		{"avatar", http.MethodPut, employeePath, map[string]string{"avatar_url": "emoji:🐨"}},
		{"skills", http.MethodPut, employeePath + "/skills", map[string][]string{"skill_ids": {}}},
		{"env", http.MethodPut, employeePath + "/env", map[string]any{"custom_env": map[string]string{}}},
		{"offers", http.MethodPut, employeePath + "/context-capabilities/offers", map[string]any{"offers": []any{}}},
	}
	for _, c := range inherited {
		w := tagDo(t, router, c.method, c.path, c.body)
		tagExpect(t, w, http.StatusConflict, "employee "+c.what)
		if !strings.Contains(w.Body.String(), tagEmployeeInheritedCode) {
			t.Fatalf("employee %s refused for another reason: %s", c.what, w.Body.String())
		}
	}
	// The inbound coordinator belongs to the tenant.
	tagExpect(t, tagDo(t, router, http.MethodPut, employeePath, map[string]bool{"inbound_coordinator": true}), http.StatusOK, "employee coordinator")
	// The template is where the shared configuration is written.
	tagExpect(t, tagDo(t, router, http.MethodPut, templatePath, map[string]string{"instructions": "shared"}), http.StatusOK, "template instructions")
	tagExpect(t, tagDo(t, router, http.MethodPut, templatePath+"/skills", map[string][]string{"skill_ids": {}}), http.StatusOK, "template skills")

	// Connector grants: an employee's membership cannot change from the
	// connector side; other agents in the same list can.
	connectorID := uuid.NewString()
	if _, err := testPool.Exec(ctx, `INSERT INTO internal_connector (id, workspace_id, name, upstream_url, credential_ref, allowed_tools, enabled)
		VALUES ($1, $2, 'TagTest connector', 'https://safe.example.test/mcp', 'REF', '["read"]'::jsonb, true)`, connectorID, testWorkspaceID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM internal_connector_agent WHERE connector_id = $1`, connectorID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM internal_connector WHERE id = $1`, connectorID)
	})
	other := createHandlerTestAgent(t, "TagTest other "+uuid.NewString()[:8], nil)
	for _, c := range []struct {
		agents []string
		want   bool
	}{
		{[]string{other}, false},
		{[]string{other, employeeID}, true},
	} {
		changed, err := tagEmployeeConnectorGrantsChanged(ctx, testPool, testWorkspaceID, connectorID, c.agents)
		if err != nil || changed != c.want {
			t.Fatalf("grants %v: changed=%v err=%v, want %v", c.agents, changed, err, c.want)
		}
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO internal_connector_agent (connector_id, workspace_id, agent_id) VALUES ($1, $2, $3)`,
		connectorID, testWorkspaceID, employeeID); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		agents []string
		want   bool
	}{
		{[]string{other, employeeID}, false},
		{[]string{other}, true},
	} {
		changed, err := tagEmployeeConnectorGrantsChanged(ctx, testPool, testWorkspaceID, connectorID, c.agents)
		if err != nil || changed != c.want {
			t.Fatalf("grants with employee granted %v: changed=%v err=%v, want %v", c.agents, changed, err, c.want)
		}
	}
}
