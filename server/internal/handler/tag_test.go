package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/middleware"
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
		r.With(RequireHumanActor).Post("/api/agents/{id}/tenants", h.CreateAgentTenant)
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
			`DELETE FROM agent_skill WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1 AND name LIKE 'TagTest%')`,
			`DELETE FROM agent_invocation_target WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id = $1 AND name LIKE 'TagTest%')`,
			`DELETE FROM tag_config_revision WHERE workspace_id = $1`,
			`DELETE FROM tag_tenant WHERE workspace_id = $1`,
			`DELETE FROM workspace_tag WHERE workspace_id = $1`,
			`DELETE FROM agent WHERE workspace_id = $1 AND name LIKE 'TagTest%'`,
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
	name := "TagTest " + uuid.NewString()[:8]

	useTagOperator(t, false)
	state := tagDecode[TagStateResponse](t, tagDo(t, router, http.MethodGet, "/api/tag", nil))
	if state.Tag != nil || state.CanOperate {
		t.Fatalf("state for non-operator = %+v", state)
	}
	tagExpect(t, tagDo(t, router, http.MethodPost, "/api/tag", map[string]string{"name": name, "runtime_id": testRuntimeID}),
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
	tagExpect(t, tagDo(t, router, http.MethodPost, "/api/tag", map[string]string{"name": name, "runtime_id": localRuntime}),
		http.StatusBadRequest, "local runtime")

	w := tagDo(t, router, http.MethodPost, "/api/tag", map[string]string{"name": name, "runtime_id": testRuntimeID, "description": "多企业数字员工"})
	tagExpect(t, w, http.StatusCreated, "operator create")
	created := tagDecode[TagStateResponse](t, w)
	if created.Tag == nil || created.Tag.Name != name || created.Tag.LatestRevision == nil || *created.Tag.LatestRevision != 1 ||
		!created.Tag.SidebarVisible || created.Tag.HasUnpublishedChanges || !created.CanOperate || !created.CanManage {
		t.Fatalf("created state = %+v", created)
	}
	tagExpect(t, tagDo(t, router, http.MethodPost, "/api/tag", map[string]string{"name": name + "2", "runtime_id": testRuntimeID}),
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
	name := "TagTest " + uuid.NewString()[:8]

	created := tagDecode[TagStateResponse](t, tagDo(t, router, http.MethodPost, "/api/tag", map[string]string{"name": name, "runtime_id": testRuntimeID}))
	templateID := created.Tag.AgentID
	if _, err := testPool.Exec(ctx, `UPDATE agent SET instructions = 'v1 instructions', persona = 'calm' WHERE id = $1`, templateID); err != nil {
		t.Fatal(err)
	}

	// A new tenant gets its own employee agent with the latest config.
	w := tagDo(t, router, http.MethodPost, "/api/tag/tenants", map[string]string{"name": "Think测试组织"})
	tagExpect(t, w, http.StatusCreated, "create tenant")
	tenant := tagDecode[TagTenantMutationResponse](t, w)
	if tenant.Tenant.EmployeeName != name+" · Think测试组织" || tenant.Tenant.Bound || tenant.Tenant.AppliedRevision == nil || *tenant.Tenant.AppliedRevision != 2 ||
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

	// Adopt an existing agent, rename, then detach it again.
	var adoptee string
	if err := testPool.QueryRow(ctx, `INSERT INTO agent (workspace_id, name, runtime_mode, runtime_config, runtime_id, visibility, permission_mode, max_concurrent_tasks, owner_id)
		VALUES ($1, 'TagTest adoptee', 'cloud', '{}'::jsonb, $2, 'workspace', 'public_to', 1, $3) RETURNING id`,
		testWorkspaceID, testRuntimeID, testUserID).Scan(&adoptee); err != nil {
		t.Fatal(err)
	}
	tagExpect(t, tagDo(t, router, http.MethodPost, "/api/tag/tenants/adopt", map[string]string{"agent_id": templateID, "name": "self"}),
		http.StatusConflict, "adopt template")
	w = tagDo(t, router, http.MethodPost, "/api/tag/tenants/adopt", map[string]string{"agent_id": adoptee, "name": "钉钉"})
	tagExpect(t, w, http.StatusCreated, "adopt")
	adopted := tagDecode[TagTenantMutationResponse](t, w)
	if adopted.Tenant.AppliedRevision != nil {
		t.Fatalf("adopted tenant applied before an apply: %+v", adopted.Tenant)
	}
	renamed := tagDecode[TagTenantMutationResponse](t, tagDo(t, router, http.MethodPatch, "/api/tag/tenants/"+adopted.Tenant.ID, map[string]string{"name": "钉钉 FDE"}))
	if renamed.Tenant.Name != "钉钉 FDE" {
		t.Fatalf("renamed = %+v", renamed.Tenant)
	}
	tagExpect(t, tagDo(t, router, http.MethodDelete, "/api/tag", nil), http.StatusConflict, "remove tag with tenants")
	tagExpect(t, tagDo(t, router, http.MethodDelete, "/api/tag/tenants/"+adopted.Tenant.ID, nil), http.StatusNoContent, "detach adoptee")
	tagExpect(t, tagDo(t, router, http.MethodDelete, "/api/tag/tenants/"+tenant.Tenant.ID, nil), http.StatusNoContent, "detach employee")
	tagExpect(t, tagDo(t, router, http.MethodDelete, "/api/tag", nil), http.StatusNoContent, "remove tag")
}
