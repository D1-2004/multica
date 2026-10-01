package contextcap

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTenantValidation(t *testing.T) {
	for _, ok := range []string{"ding1234", "org_A-9", strings.Repeat("a", 64)} {
		if !ValidTenantOrgID(ok) {
			t.Errorf("ValidTenantOrgID(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "org 1", "org.1", "组织", strings.Repeat("a", 65)} {
		if ValidTenantOrgID(bad) {
			t.Errorf("ValidTenantOrgID(%q) = true", bad)
		}
	}
	if name, ok := NormalizeTenantName("  钉钉科技 "); !ok || name != "钉钉科技" {
		t.Fatalf("NormalizeTenantName = %q %v", name, ok)
	}
	for _, bad := range []string{"", "   ", "a\nb", strings.Repeat("名", MaxTenantName+1), "\xff"} {
		if _, ok := NormalizeTenantName(bad); ok {
			t.Errorf("NormalizeTenantName(%q) accepted", bad)
		}
	}
	ctx := context.Background()
	if _, err := CreateTenant(ctx, nil, TenantWrite{OrgID: "bad org", Name: "x"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("create with a bad org: %v", err)
	}
	if _, err := RenameTenant(ctx, nil, TenantWrite{OrgID: "org", Name: ""}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("rename to an empty name: %v", err)
	}
}

// tenantFixture binds the store fixture's agent to identityOrg.
func (f storeFixture) bindIdentity(t *testing.T, identityOrg, orgName string) string {
	t.Helper()
	ctx := context.Background()
	var user string
	if err := f.tx.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ('Tenant Admin', 'tenant-' || $1 || '@example.test') RETURNING id::text`,
		uuid.NewString()).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tx.Exec(ctx, `INSERT INTO agent_dingtalk_identity (agent_id, workspace_id, dws_uid, org_id, bound_by, organization_name)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5::uuid, $6)`, f.agentID, f.workspaceID, "dws-"+uuid.NewString(), identityOrg, user, orgName); err != nil {
		t.Fatal(err)
	}
	return user
}

func tenantMigrated(t *testing.T, f storeFixture) {
	t.Helper()
	var migrated bool
	if err := f.tx.QueryRow(context.Background(), `SELECT to_regclass('agent_tenant') IS NOT NULL AND to_regclass('context_prompt_component') IS NOT NULL`).Scan(&migrated); err != nil || !migrated {
		t.Skip("tenant tables are not migrated")
	}
}

func TestAgentTenantsLifecycle(t *testing.T) {
	f := openStoreTx(t)
	tenantMigrated(t, f)
	ctx := context.Background()

	// Without an identity and without rows the agent has no tenant.
	tenants, err := AgentTenants(ctx, f.tx, f.workspaceID, f.agentID)
	if err != nil || len(tenants) != 0 {
		t.Fatalf("tenants=%+v err=%v", tenants, err)
	}
	actor := f.bindIdentity(t, "org-home", "Home Corp")
	tenants, err = AgentTenants(ctx, f.tx, f.workspaceID, f.agentID)
	if err != nil || len(tenants) != 1 || tenants[0].OrgID != "org-home" || tenants[0].Name != "Home Corp" ||
		tenants[0].Source != TenantSourceIdentity || tenants[0].HasRow {
		t.Fatalf("identity tenant=%+v err=%v", tenants, err)
	}

	write := TenantWrite{WorkspaceID: f.workspaceID, AgentID: f.agentID, OrgID: "org-b", Name: " Beta ", ActorID: actor}
	created, err := CreateTenant(ctx, f.tx, write)
	if err != nil || created.Name != "Beta" || created.Source != TenantSourceCreated || !created.HasRow || created.CreatedBy != actor {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if _, err := CreateTenant(ctx, f.tx, write); !errors.Is(err, ErrTenantExists) {
		t.Fatalf("duplicate create: %v", err)
	}
	if _, err := CreateTenant(ctx, f.tx, TenantWrite{WorkspaceID: f.workspaceID, AgentID: f.agentID, OrgID: "org-home", Name: "Again"}); !errors.Is(err, ErrTenantExists) {
		t.Fatalf("create the identity org: %v", err)
	}
	if _, err := CreateTenant(ctx, f.tx, TenantWrite{WorkspaceID: f.workspaceID, AgentID: f.agentID, OrgID: "org-a", Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	tenants, err = AgentTenants(ctx, f.tx, f.workspaceID, f.agentID)
	if err != nil || len(tenants) != 3 || tenants[0].OrgID != "org-home" || tenants[1].OrgID != "org-a" || tenants[2].OrgID != "org-b" {
		t.Fatalf("ordered tenants=%+v err=%v", tenants, err)
	}

	// Renaming the identity tenant stores a row; it stays the identity.
	renamed, err := RenameTenant(ctx, f.tx, TenantWrite{WorkspaceID: f.workspaceID, AgentID: f.agentID, OrgID: "org-home", Name: "总部", ActorID: actor})
	if err != nil || renamed.Name != "总部" || renamed.Source != TenantSourceIdentity {
		t.Fatalf("renamed identity=%+v err=%v", renamed, err)
	}
	if tenant, err := AgentTenant(ctx, f.tx, f.workspaceID, f.agentID, "org-home"); err != nil || tenant.Name != "总部" || !tenant.HasRow || tenant.Source != TenantSourceIdentity {
		t.Fatalf("identity after rename=%+v err=%v", tenant, err)
	}
	if _, err := RenameTenant(ctx, f.tx, TenantWrite{WorkspaceID: f.workspaceID, AgentID: f.agentID, OrgID: "org-missing", Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rename unknown: %v", err)
	}
	if _, err := AgentTenant(ctx, f.tx, f.workspaceID, f.agentID, "org-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown tenant: %v", err)
	}

	// Deleting a tenant drops its org-scope configuration only.
	if err := ReplaceOffers(ctx, f.tx, f.workspaceID, f.agentID, []string{f.connectorID}, []string{f.skillID}, actor); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []struct{ scopeType, key string }{{ScopeOrg, "org-b"}, {ScopeScene, "cidBetaGroup"}} {
		if _, err := UpsertBinding(ctx, f.tx, BindingWrite{WorkspaceID: f.workspaceID, AgentID: f.agentID, ScopeType: scope.scopeType,
			OrgID: "org-b", ScopeKey: scope.key, ResourceType: ResourceSkill, ResourceID: f.skillID, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := ReplacePromptComponents(ctx, f.tx, PromptComponentsWrite{WorkspaceID: f.workspaceID, AgentID: f.agentID,
			ScopeType: scope.scopeType, OrgID: "org-b", ScopeKey: scope.key, Components: []PromptComponentInput{{Name: "p", Text: "t"}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := PutScopeMCPConfig(ctx, f.tx, ScopeMCPConfigWrite{WorkspaceID: f.workspaceID, AgentID: f.agentID, ScopeType: scope.scopeType,
			OrgID: "org-b", ScopeKey: scope.key, MCPConfig: json.RawMessage(`{"mcpServers":{"a":{"url":"https://a"}}}`)}); err != nil {
			t.Fatal(err)
		}
		if _, err := UpsertCredential(ctx, f.tx, CredentialBinding{WorkspaceID: f.workspaceID, AgentID: f.agentID, ConnectorID: f.connectorID,
			ScopeType: scope.scopeType, OrgID: "org-b", ScopeKey: scope.key}, []byte{1}, "x", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := DeleteTenant(ctx, f.tx, f.workspaceID, f.agentID, "org-home"); !errors.Is(err, ErrIdentityTenant) {
		t.Fatalf("delete identity: %v", err)
	}
	if err := DeleteTenant(ctx, f.tx, f.workspaceID, f.agentID, "org-b"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteTenant(ctx, f.tx, f.workspaceID, f.agentID, "org-b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	for _, table := range []string{"context_capability_binding", "context_prompt_component", "context_scope_mcp_config", "context_connector_credential"} {
		var orgRows, sceneRows int
		if err := f.tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE scope_type = 'org'), count(*) FILTER (WHERE scope_type = 'scene')
			FROM `+table+` WHERE agent_id = $1::uuid AND org_id = 'org-b'`, f.agentID).Scan(&orgRows, &sceneRows); err != nil {
			t.Fatal(err)
		}
		if orgRows != 0 || sceneRows != 1 {
			t.Errorf("%s after delete: org=%d scene=%d", table, orgRows, sceneRows)
		}
	}
}

func TestOrgActivityAndPersons(t *testing.T) {
	f := openStoreTx(t)
	tenantMigrated(t, f)
	ctx := context.Background()
	f.bindIdentity(t, "org-home", "")

	// org-home: a group job, a 1:1 job with a sender recorded without an
	// org (it belongs to the identity), a person binding.
	f.groupJob(t, "cidHomeGroup", "org-home", time.Hour)
	f.directJob(t, "cidHomeDirect", "staff-ann", "Ann", "", 2*time.Hour)
	if err := ReplaceOffers(ctx, f.tx, f.workspaceID, f.agentID, nil, []string{f.skillID}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertBinding(ctx, f.tx, BindingWrite{WorkspaceID: f.workspaceID, AgentID: f.agentID, ScopeType: ScopePerson,
		OrgID: "org-home", ScopeKey: "staff-bo", ScopeTitle: "Bo", ResourceType: ResourceSkill, ResourceID: f.skillID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// org-x (not a tenant): a group job and a scene memory row.
	f.groupJob(t, "cidForeignGroup", "org-x", time.Minute)
	if _, err := f.tx.Exec(ctx, `INSERT INTO scene_memory (workspace_id, agent_id, platform, org_id, scene_key, scene_kind)
		VALUES ($1::uuid, $2::uuid, 'dingtalk', 'org-x', 'cidForeignMemory', 'group')`, f.workspaceID, f.agentID); err != nil {
		t.Fatal(err)
	}
	// A person link redeemed in a 1:1 chat under org-x.
	f.directLink(t, "org-x", "cidForeignDirect", "staff-cy", "Cy", uuid.NewString(), time.Hour)

	activity, err := ListAgentOrgActivity(ctx, f.tx, f.workspaceID, f.agentID, "org-home")
	if err != nil {
		t.Fatal(err)
	}
	if got := activity["org-home"]; got.GroupCount != 1 || got.PersonCount != 2 {
		t.Fatalf("org-home activity=%+v", got)
	}
	if got := activity["org-x"]; got.GroupCount != 2 || got.PersonCount != 1 {
		t.Fatalf("org-x activity=%+v", got)
	}
	if _, ok := activity[""]; ok {
		t.Fatal("empty org listed")
	}

	persons, err := ListOrgPersons(ctx, f.tx, f.workspaceID, f.agentID, "org-home", "org-home")
	if err != nil || len(persons) != 2 {
		t.Fatalf("org-home persons=%+v err=%v", persons, err)
	}
	ann, ok := FindPerson(persons, "staff-ann")
	if !ok || ann.Title != "Ann" || ann.DMSceneKey != "cidHomeDirect" || ann.LastActiveAt.IsZero() {
		t.Fatalf("ann=%+v", ann)
	}
	if bo, ok := FindPerson(persons, "staff-bo"); !ok || bo.Title != "Bo" || bo.DMSceneKey != "" {
		t.Fatalf("bo=%+v", bo)
	}
	// The unrecorded 1:1 job belongs to the identity only.
	persons, err = ListOrgPersons(ctx, f.tx, f.workspaceID, f.agentID, "org-x", "org-home")
	if err != nil || len(persons) != 1 || persons[0].StaffID != "staff-cy" || persons[0].Title != "Cy" || persons[0].DMSceneKey != "cidForeignDirect" {
		t.Fatalf("org-x persons=%+v err=%v", persons, err)
	}

	// The scene list of a non-identity org skips jobs without a recorded org.
	scenes, _, err := ListAgentScenes(ctx, f.tx, SceneListQuery{WorkspaceID: f.workspaceID, AgentID: f.agentID, OrgID: "org-x", IdentityOrgID: "org-home"})
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{}
	for _, scene := range scenes {
		keys[scene.SceneKey] = scene.Kind
	}
	if len(keys) != 2 || keys["cidForeignGroup"] != SceneKindGroup || keys["cidForeignMemory"] != SceneKindGroup {
		t.Fatalf("org-x scenes=%v", keys)
	}
	groups, _, err := ListAgentScenes(ctx, f.tx, SceneListQuery{WorkspaceID: f.workspaceID, AgentID: f.agentID, OrgID: "org-home",
		IdentityOrgID: "org-home", GroupsOnly: true})
	if err != nil || len(groups) != 1 || groups[0].SceneKey != "cidHomeGroup" {
		t.Fatalf("org-home groups=%+v err=%v", groups, err)
	}

	// A person known only from an expired grant is no longer listed or
	// counted.
	if _, err := f.tx.Exec(ctx, `INSERT INTO context_config_grant (user_id, workspace_id, agent_id, scope_type, org_id, scope_key, scope_title, source, expires_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'person', 'org-home', 'staff-expired', 'Gone', 'agent_link', now() - interval '1 day')`,
		uuid.NewString(), f.workspaceID, f.agentID); err != nil {
		t.Fatal(err)
	}
	persons, err = ListOrgPersons(ctx, f.tx, f.workspaceID, f.agentID, "org-home", "org-home")
	if err != nil || len(persons) != 2 {
		t.Fatalf("org-home persons with an expired grant=%+v err=%v", persons, err)
	}
	if _, ok := FindPerson(persons, "staff-expired"); ok {
		t.Fatal("a person known only from an expired grant is listed")
	}
	activity, err = ListAgentOrgActivity(ctx, f.tx, f.workspaceID, f.agentID, "org-home")
	if err != nil || activity["org-home"].PersonCount != 2 {
		t.Fatalf("org-home activity with an expired grant=%+v err=%v", activity["org-home"], err)
	}
}

// groupJob plants a group Coordinator job of cid recorded under
// dispatchOrg, with its chat session.
func (f storeFixture) groupJob(t *testing.T, cid, dispatchOrg string, age time.Duration) {
	t.Helper()
	ctx := context.Background()
	command, err := json.Marshal(map[string]any{
		"source": map[string]any{"platform": "dingtalk", "type": "digital_employee"},
		"event": map[string]any{"data": map[string]any{
			"conversation": map[string]any{"openConversationId": cid, "type": "group", "title": "Group " + cid},
			"sender":       map[string]any{"staffId": "staff-group-sender", "displayName": "Sender"},
		}},
		"externalIdentity": map[string]any{"dws": map[string]any{"orgId": dispatchOrg}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var user, session string
	if err := f.tx.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ('Job user', 'job-' || $1 || '@example.test') RETURNING id::text`,
		uuid.NewString()).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.QueryRow(ctx, `INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, updated_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'coordinator', now() - make_interval(secs => $4::double precision)) RETURNING id::text`,
		f.workspaceID, f.agentID, user, age.Seconds()).Scan(&session); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tx.Exec(ctx, `INSERT INTO inbound_coordinator_job
		(acceptance_id, workspace_id, agent_id, user_id, endpoint_namespace_id, idempotency_key, command, chat_session_id, user_message_id, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'completed', now() - make_interval(secs => $10::double precision))`,
		uuid.NewString(), f.workspaceID, f.agentID, user, uuid.NewString(), uuid.NewString(), command,
		session, uuid.NewString(), age.Seconds()); err != nil {
		t.Fatal(err)
	}
}
