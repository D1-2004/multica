package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type staticFCE2BTemplateRunner struct {
	output string
	err    error
	calls  int
}

func (r *staticFCE2BTemplateRunner) Run(context.Context, string, []string, []string) (string, error) {
	r.calls++
	return r.output, r.err
}

func TestFCE2BTemplateCapabilities(t *testing.T) {
	cases := []struct {
		template service.FCE2BTemplate
		want     []string
	}{
		{service.FCE2BTemplate{Template: "multica-fc-hermes-v1"}, []string{"hermes"}},
		{service.FCE2BTemplate{Template: "multica-fc-hermes-dws-v1"}, []string{"hermes", "dws"}},
		{service.FCE2BTemplate{Template: "multica-fc-opencode-v1"}, []string{"opencode"}},
		{service.FCE2BTemplate{Template: "multica-fc-opencode-dws-v1"}, []string{"opencode", "dws"}},
		{service.FCE2BTemplate{Template: "custom-team-template", Name: "OpenCode Team"}, []string{"opencode"}},
	}
	for _, tc := range cases {
		t.Run(tc.template.Template, func(t *testing.T) {
			provider := service.FCE2BProviderForTemplate(tc.template.Template, tc.template.ID, tc.template.Name)
			if got := fcE2BTemplateCapabilities(provider, tc.template); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("fcE2BTemplateCapabilities(%q) = %#v, want %#v", tc.template.Template, got, tc.want)
			}
		})
	}
}

func TestResolveFCE2BProvider(t *testing.T) {
	dual := service.FCE2BTemplate{Template: "multica-fc-team-v1"}
	cases := []struct {
		requested string
		template  service.FCE2BTemplate
		want      string
		ok        bool
	}{
		{"", dual, "hermes", true},
		{"", service.FCE2BTemplate{Template: "multica-fc-opencode-v1"}, "opencode", true},
		{"hermes", dual, "hermes", true},
		{"opencode", dual, "opencode", true},
		{" OpenCode ", dual, "opencode", true},
		// An explicit provider wins over the template-name default.
		{"hermes", service.FCE2BTemplate{Template: "multica-fc-opencode-v1"}, "hermes", true},
		{"codex", dual, "", false},
		{"claude", dual, "", false},
	}
	for _, tc := range cases {
		got, ok := resolveFCE2BProvider(tc.requested, tc.template)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("resolveFCE2BProvider(%q, %q) = (%q, %v), want (%q, %v)",
				tc.requested, tc.template.Template, got, ok, tc.want, tc.ok)
		}
	}
}

func TestDefaultFCE2BRuntimeName(t *testing.T) {
	cases := []struct {
		provider string
		template service.FCE2BTemplate
		want     string
	}{
		{"hermes", service.FCE2BTemplate{Template: "multica-fc-hermes-v1"}, "FC-Hermes-V1"},
		{"opencode", service.FCE2BTemplate{Template: "multica-fc-opencode-v1"}, "FC-Opencode-V1"},
		// Dual-CLI template: the provider is prefixed so the two default
		// names cannot collide.
		{"hermes", service.FCE2BTemplate{Template: "multica-fc-team-v1"}, "FC-Hermes-Team-V1"},
		{"opencode", service.FCE2BTemplate{Template: "multica-fc-team-v1"}, "FC-Opencode-Team-V1"},
		{"hermes", service.FCE2BTemplate{}, "FC-Hermes"},
		{"opencode", service.FCE2BTemplate{}, "FC-Opencode"},
	}
	for _, tc := range cases {
		if got := defaultFCE2BRuntimeName(tc.provider, tc.template); got != tc.want {
			t.Fatalf("defaultFCE2BRuntimeName(%q, %q) = %q, want %q",
				tc.provider, tc.template.Template, got, tc.want)
		}
	}
}

func TestRuntimeSlug(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"FC-Hermes", "fc-hermes"},
		{"  FC  Hermes!!!  ", "fc-hermes"},
		{"中文 Hermes", "hermes"},
		{"中文", "fc-hermes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := runtimeSlug(tc.name); got != tc.want {
				t.Fatalf("runtimeSlug(%q) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func fce2bTemplateRotationHandler(t *testing.T) (*Handler, *staticFCE2BTemplateRunner) {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("handler database fixture unavailable")
	}
	runner := &staticFCE2BTemplateRunner{output: `[
		{"id":"tpl_old_id","name":"Old Template","template":"tpl_old","status":"ready"},
		{"id":"tpl_new_id","name":"New DWS Template","template":"tpl_new_dws","status":"READY"},
		{"id":"tpl_pending_id","name":"Pending Template","template":"tpl_pending","status":"building"}
	]`}
	h := *testHandler
	launcher := *testHandler.FCE2BLauncher
	launcher.Runner = runner
	h.FCE2BLauncher = &launcher
	h.cfg.FCE2B = service.FCE2BConfig{
		Enabled: true,
		APIKey:  "test-api-key",
		APIURL:  "https://fc-e2b.test",
		Domain:  "fc-e2b.test",
		CLIPath: "e2b-test",
	}
	return &h, runner
}

func fce2bTemplateRotationFixture(t *testing.T) (runtimeID, runtimeOwnerID, agentID string) {
	t.Helper()
	ctx := context.Background()
	suffix := randomID()[:8]
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email)
		VALUES ('FC Runtime Owner', $1)
		RETURNING id
	`, fmt.Sprintf("fc-runtime-owner-%s@multica.test", suffix)).Scan(&runtimeOwnerID); err != nil {
		t.Fatalf("create runtime owner: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'member')
	`, testWorkspaceID, runtimeOwnerID); err != nil {
		t.Fatalf("create runtime owner membership: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, owner_id, visibility, last_seen_at
		) VALUES (
			$1, $2, 'FC Template Rotation Runtime', 'cloud', 'hermes', 'online',
			'template rotation',
			'{"kind":"fc-e2b","template":"tpl_old","template_id":"tpl_old_id","template_name":"Old Template","template_status":"ready","capabilities":["hermes"],"runner":"multica-fc-hermes-runner","preserved":"yes"}'::jsonb,
			$3, 'private', now()
		) RETURNING id
	`, testWorkspaceID, "fc-e2b:rotation:"+suffix, runtimeOwnerID).Scan(&runtimeID); err != nil {
		t.Fatalf("create FC/E2B runtime: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, max_concurrent_tasks, owner_id,
			instructions, custom_env, custom_args
		) VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 1, $4, '', '{}'::jsonb, '[]'::jsonb)
		RETURNING id
	`, testWorkspaceID, "fc-template-rotation-agent-"+suffix, runtimeID, runtimeOwnerID).Scan(&agentID); err != nil {
		t.Fatalf("create bound Agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, runtimeOwnerID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, runtimeOwnerID)
	})
	return runtimeID, runtimeOwnerID, agentID
}

func patchFCE2BRuntimeTemplate(h *Handler, actorID, runtimeID, templateID string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := newRequestAs(actorID, http.MethodPatch, "/api/runtimes/"+runtimeID+"/fc-e2b-template", map[string]any{
		"template_id": templateID,
	})
	req = withURLParam(req, "runtimeId", runtimeID)
	h.UpdateFCE2BRuntimeTemplate(w, req)
	return w
}

func TestUpdateFCE2BRuntimeTemplateRequiresWorkspaceOwnerOrAdmin(t *testing.T) {
	h, _ := fce2bTemplateRotationHandler(t)
	runtimeID, runtimeOwnerID, _ := fce2bTemplateRotationFixture(t)

	if w := patchFCE2BRuntimeTemplate(h, runtimeOwnerID, runtimeID, "tpl_new_id"); w.Code != http.StatusForbidden {
		t.Fatalf("plain runtime owner status = %d, want 403: %s", w.Code, w.Body.String())
	}
	if w := patchFCE2BRuntimeTemplate(h, testUserID, runtimeID, "tpl_new_id"); w.Code != http.StatusOK {
		t.Fatalf("workspace owner status = %d, want 200: %s", w.Code, w.Body.String())
	}

	ctx := context.Background()
	var adminID string
	if err := testPool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ('FC Runtime Admin', $1) RETURNING id`, "fc-runtime-admin-"+randomID()[:8]+"@multica.test").Scan(&adminID); err != nil {
		t.Fatalf("create workspace admin: %v", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'admin')`, testWorkspaceID, adminID); err != nil {
		t.Fatalf("create workspace admin membership: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, adminID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, adminID)
	})
	if w := patchFCE2BRuntimeTemplate(h, adminID, runtimeID, "tpl_new_id"); w.Code != http.StatusOK {
		t.Fatalf("workspace admin status = %d, want 200: %s", w.Code, w.Body.String())
	}
}

func TestUpdateFCE2BRuntimeTemplateValidatesStrictCatalogIDAndReadyStatus(t *testing.T) {
	h, runner := fce2bTemplateRotationHandler(t)
	runtimeID, _, _ := fce2bTemplateRotationFixture(t)

	for name, templateID := range map[string]string{
		"template alias": "tpl_new_dws",
		"unknown id":     "tpl_unknown_id",
		"empty id":       " ",
		"not ready":      "tpl_pending_id",
	} {
		t.Run(name, func(t *testing.T) {
			w := patchFCE2BRuntimeTemplate(h, testUserID, runtimeID, templateID)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
		})
	}
	if runner.calls == 0 {
		t.Fatal("template catalog runner was not used")
	}

	if _, err := testPool.Exec(context.Background(), `UPDATE agent_runtime SET metadata = '{"kind":"other"}'::jsonb WHERE id = $1`, runtimeID); err != nil {
		t.Fatalf("change runtime kind: %v", err)
	}
	if w := patchFCE2BRuntimeTemplate(h, testUserID, runtimeID, "tpl_new_id"); w.Code != http.StatusBadRequest {
		t.Fatalf("non-FC/E2B runtime status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

func TestUpdateFCE2BRuntimeTemplatePreservesRuntimeAndIsIdempotent(t *testing.T) {
	h, _ := fce2bTemplateRotationHandler(t)
	runtimeID, runtimeOwnerID, agentID := fce2bTemplateRotationFixture(t)
	runtimeUUID := util.MustParseUUID(runtimeID)
	scopeID := util.MustParseUUID(runtimeOwnerID)
	if _, err := h.Queries.UpsertFCE2BSandboxSession(context.Background(), db.UpsertFCE2BSandboxSessionParams{
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		RuntimeID:   runtimeUUID,
		ScopeType:   "chat",
		ScopeID:     scopeID,
		SandboxID:   "sbx_old_template",
		Template:    "tpl_old",
		ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("seed reusable old sandbox: %v", err)
	}

	w := patchFCE2BRuntimeTemplate(h, testUserID, runtimeID, "tpl_new_id")
	if w.Code != http.StatusOK {
		t.Fatalf("template update status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var response AgentRuntimeResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode runtime response: %v", err)
	}
	if response.ID != runtimeID || response.Provider != "hermes" || response.OwnerID == nil || *response.OwnerID != runtimeOwnerID {
		t.Fatalf("runtime identity changed: %+v", response)
	}

	runtime, err := h.Queries.GetAgentRuntime(context.Background(), runtimeUUID)
	if err != nil {
		t.Fatalf("load updated runtime: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(runtime.Metadata, &metadata); err != nil {
		t.Fatalf("decode runtime metadata: %v", err)
	}
	if metadata["template"] != "tpl_new_dws" || metadata["template_id"] != "tpl_new_id" || metadata["template_name"] != "New DWS Template" || metadata["template_status"] != "READY" {
		t.Fatalf("template metadata = %#v", metadata)
	}
	if metadata["preserved"] != "yes" || metadata["runner"] != "multica-fc-hermes-runner" {
		t.Fatalf("unrelated metadata was not preserved: %#v", metadata)
	}
	if got := metadata["capabilities"]; !reflect.DeepEqual(got, []any{"hermes"}) {
		t.Fatalf("capabilities changed during template rotation: %#v", got)
	}
	var boundRuntimeID string
	if err := testPool.QueryRow(context.Background(), `SELECT runtime_id FROM agent WHERE id = $1`, agentID).Scan(&boundRuntimeID); err != nil {
		t.Fatalf("load Agent binding: %v", err)
	}
	if boundRuntimeID != runtimeID {
		t.Fatalf("Agent runtime binding = %s, want %s", boundRuntimeID, runtimeID)
	}
	var status string
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM fc_e2b_sandbox_session WHERE runtime_id = $1 AND scope_type = 'chat' AND scope_id = $2`, runtimeID, runtimeOwnerID).Scan(&status); err != nil {
		t.Fatalf("load invalidated sandbox: %v", err)
	}
	if status != "stale" {
		t.Fatalf("old sandbox status = %q, want stale", status)
	}

	if _, err := h.Queries.UpsertFCE2BSandboxSession(context.Background(), db.UpsertFCE2BSandboxSessionParams{
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		RuntimeID:   runtimeUUID,
		ScopeType:   "chat",
		ScopeID:     scopeID,
		SandboxID:   "sbx_new_template",
		Template:    "tpl_new_dws",
		ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("seed current-template sandbox: %v", err)
	}
	w = patchFCE2BRuntimeTemplate(h, testUserID, runtimeID, "tpl_new_id")
	if w.Code != http.StatusOK {
		t.Fatalf("idempotent update status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if err := testPool.QueryRow(context.Background(), `SELECT status FROM fc_e2b_sandbox_session WHERE runtime_id = $1 AND scope_type = 'chat' AND scope_id = $2`, runtimeID, runtimeOwnerID).Scan(&status); err != nil {
		t.Fatalf("load sandbox after idempotent update: %v", err)
	}
	if status != "running" {
		t.Fatalf("same-template update changed sandbox status to %q", status)
	}
}

func TestSelectFCE2BTemplateByIDDoesNotMatchAliases(t *testing.T) {
	templates := []service.FCE2BTemplate{{ID: "tpl_real_id", Template: "template-alias", Name: "Display Name", Status: "ready"}}
	if _, ok := selectFCE2BTemplateByID(templates, "template-alias"); ok {
		t.Fatal("template alias matched strict ID selector")
	}
	if _, ok := selectFCE2BTemplateByID(templates, "Display Name"); ok {
		t.Fatal("template name matched strict ID selector")
	}
	if selected, ok := selectFCE2BTemplateByID(templates, "tpl_real_id"); !ok || selected.ID != "tpl_real_id" {
		t.Fatalf("real template ID did not match: selected=%+v ok=%v", selected, ok)
	}
}

func TestUpdateFCE2BRuntimeTemplateCatalogFailureIsExplicit(t *testing.T) {
	h, runner := fce2bTemplateRotationHandler(t)
	runtimeID, _, _ := fce2bTemplateRotationFixture(t)
	runner.err = errors.New("catalog unavailable")
	w := patchFCE2BRuntimeTemplate(h, testUserID, runtimeID, "tpl_new_id")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("catalog failure status = %d, want 503: %s", w.Code, w.Body.String())
	}
}
