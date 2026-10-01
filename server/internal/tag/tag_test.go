package tag

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixture struct {
	tx          pgx.Tx
	workspaceID string
	userID      string
	templateID  string
	employeeID  string
	skillID     string
	connectorID string
}

func openTx(t *testing.T) fixture {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database unreachable: %v", err)
	}
	t.Cleanup(pool.Close)
	var migrated bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('workspace_tag') IS NOT NULL
		AND to_regclass('tag_tenant') IS NOT NULL AND to_regclass('tag_config_revision') IS NOT NULL`).Scan(&migrated); err != nil || !migrated {
		t.Skip("tag tables are not migrated")
	}
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

	bg := context.Background()
	f := fixture{tx: tx}
	suffix := uuid.NewString()
	if err := tx.QueryRow(bg, `INSERT INTO workspace (name, slug, description) VALUES ('tag', 'tag-' || $1, '') RETURNING id::text`,
		suffix).Scan(&f.workspaceID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(bg, `INSERT INTO "user" (name, email) VALUES ('tag owner', 'tag-' || $1 || '@example.test') RETURNING id::text`,
		suffix).Scan(&f.userID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(bg, `INSERT INTO agent (workspace_id, name, runtime_mode, instructions, model, persona, mcp_config, inbound_coordinator)
		VALUES ($1::uuid, 'QwenTag', 'cloud', 'shared instructions', 'model-a', 'friendly', '{"mcpServers":{}}'::jsonb, TRUE)
		RETURNING id::text`, f.workspaceID).Scan(&f.templateID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(bg, `INSERT INTO agent (workspace_id, name, runtime_mode, instructions, model, permission_mode)
		VALUES ($1::uuid, 'QwenTag · Think', 'cloud', 'old employee instructions', 'model-old', 'public_to')
		RETURNING id::text`, f.workspaceID).Scan(&f.employeeID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(bg, `INSERT INTO skill (workspace_id, name, description, content) VALUES ($1::uuid, 'tag-skill-' || $2, 'desc', 'body') RETURNING id::text`,
		f.workspaceID, suffix).Scan(&f.skillID); err != nil {
		t.Fatal(err)
	}
	f.connectorID = uuid.NewString()
	if _, err := tx.Exec(bg, `INSERT INTO internal_connector (id, workspace_id, name, upstream_url, credential_ref, allowed_tools, enabled)
		VALUES ($1::uuid, $2::uuid, 'Knowledge', 'https://safe.example.test/mcp', 'REF', '["read"]'::jsonb, true)`, f.connectorID, f.workspaceID); err != nil {
		t.Fatal(err)
	}
	// Template shared config: one skill, one granted connector, one offer.
	if _, err := tx.Exec(bg, `INSERT INTO agent_skill (agent_id, skill_id, enabled) VALUES ($1::uuid, $2::uuid, TRUE)`, f.templateID, f.skillID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(bg, `INSERT INTO internal_connector_agent (connector_id, workspace_id, agent_id) VALUES ($1::uuid, $2::uuid, $3::uuid)`,
		f.connectorID, f.workspaceID, f.templateID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(bg, `INSERT INTO context_capability_binding (workspace_id, agent_id, scope_type, resource_type, resource_id, enabled)
		VALUES ($1::uuid, $2::uuid, 'offer', 'skill', $3::uuid, TRUE)`, f.workspaceID, f.templateID, f.skillID); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestTagIsOnePerWorkspace(t *testing.T) {
	f := openTx(t)
	ctx := context.Background()
	if _, err := Get(ctx, f.tx, f.workspaceID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get before create = %v, want ErrNotFound", err)
	}
	created, err := Create(ctx, f.tx, f.workspaceID, f.templateID, f.userID)
	if err != nil {
		t.Fatal(err)
	}
	if !created.SidebarVisible || created.AgentID != f.templateID {
		t.Fatalf("created tag = %+v", created)
	}
	if _, err := Create(ctx, f.tx, f.workspaceID, f.employeeID, f.userID); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("second Create = %v, want ErrAlreadyExists", err)
	}
	role, err := AgentRole(ctx, f.tx, f.workspaceID, f.templateID)
	if err != nil || role != RoleTemplate {
		t.Fatalf("template role = %q, %v", role, err)
	}
	hidden, err := SetSidebarVisible(ctx, f.tx, f.workspaceID, false)
	if err != nil || hidden.SidebarVisible {
		t.Fatalf("SetSidebarVisible = %+v, %v", hidden, err)
	}
}

func TestTenantEmployeeRules(t *testing.T) {
	f := openTx(t)
	ctx := context.Background()
	if _, err := Create(ctx, f.tx, f.workspaceID, f.templateID, f.userID); err != nil {
		t.Fatal(err)
	}
	if _, err := InsertTenant(ctx, f.tx, f.workspaceID, f.templateID, f.templateID, "Think", f.userID); !errors.Is(err, ErrAgentInUse) {
		t.Fatalf("template as employee = %v, want ErrAgentInUse", err)
	}
	if _, err := InsertTenant(ctx, f.tx, f.workspaceID, f.templateID, f.employeeID, "  ", f.userID); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("blank name = %v, want ErrInvalidName", err)
	}
	tenant, err := InsertTenant(ctx, f.tx, f.workspaceID, f.templateID, f.employeeID, " Think测试组织 ", f.userID)
	if err != nil {
		t.Fatal(err)
	}
	if tenant.Name != "Think测试组织" || tenant.Bound() || tenant.AppliedRevision != nil {
		t.Fatalf("new tenant = %+v", tenant)
	}
	if _, err := InsertTenant(ctx, f.tx, f.workspaceID, f.templateID, f.employeeID, "Again", f.userID); !errors.Is(err, ErrAgentInUse) {
		t.Fatalf("duplicate employee = %v, want ErrAgentInUse", err)
	}
	role, err := AgentRole(ctx, f.tx, f.workspaceID, f.employeeID)
	if err != nil || role != RoleEmployee {
		t.Fatalf("employee role = %q, %v", role, err)
	}

	// The tenant's enterprise is the employee's bound digital-employee org.
	if _, err := f.tx.Exec(ctx, `INSERT INTO agent_dingtalk_identity (agent_id, workspace_id, dws_uid, org_id, account_display_name, organization_name, bound_by)
		VALUES ($1::uuid, $2::uuid, '1001', '177928186', 'QwenTag', 'Think测试组织', $3::uuid)`, f.employeeID, f.workspaceID, f.userID); err != nil {
		t.Fatal(err)
	}
	tenants, err := ListTenants(ctx, f.tx, f.workspaceID, f.templateID)
	if err != nil || len(tenants) != 1 {
		t.Fatalf("ListTenants = %+v, %v", tenants, err)
	}
	if !tenants[0].Bound() || tenants[0].OrgID != "177928186" || tenants[0].OrganizationName != "Think测试组织" {
		t.Fatalf("bound tenant = %+v", tenants[0])
	}

	var archived string
	if err := f.tx.QueryRow(ctx, `INSERT INTO agent (workspace_id, name, runtime_mode, archived_at) VALUES ($1::uuid, 'gone', 'cloud', now()) RETURNING id::text`,
		f.workspaceID).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if _, err := InsertTenant(ctx, f.tx, f.workspaceID, f.templateID, archived, "Gone", f.userID); !errors.Is(err, ErrAgentUnavailable) {
		t.Fatalf("archived employee = %v, want ErrAgentUnavailable", err)
	}

	if err := Delete(ctx, f.tx, f.workspaceID); !errors.Is(err, ErrHasTenants) {
		t.Fatalf("Delete with tenants = %v, want ErrHasTenants", err)
	}
	if err := DeleteTenant(ctx, f.tx, f.workspaceID, tenant.ID); err != nil {
		t.Fatal(err)
	}
	if err := Delete(ctx, f.tx, f.workspaceID); err != nil {
		t.Fatalf("Delete without tenants = %v", err)
	}
}

func TestPublishOnlyWhenTemplateChanges(t *testing.T) {
	f := openTx(t)
	ctx := context.Background()
	changed, err := HasUnpublishedChanges(ctx, f.tx, f.workspaceID, f.templateID)
	if err != nil || !changed {
		t.Fatalf("before first publish changed = %v, %v", changed, err)
	}
	r1, created, err := PublishIfChanged(ctx, f.tx, f.workspaceID, f.templateID, f.userID, "first")
	if err != nil || !created || r1.Revision != 1 {
		t.Fatalf("first publish = %+v created=%v err=%v", r1, created, err)
	}
	again, created, err := PublishIfChanged(ctx, f.tx, f.workspaceID, f.templateID, f.userID, "")
	if err != nil || created || again.Revision != 1 {
		t.Fatalf("unchanged publish = %+v created=%v err=%v", again, created, err)
	}
	if changed, err := HasUnpublishedChanges(ctx, f.tx, f.workspaceID, f.templateID); err != nil || changed {
		t.Fatalf("after publish changed = %v, %v", changed, err)
	}
	if _, err := f.tx.Exec(ctx, `UPDATE agent SET instructions = 'shared instructions v2' WHERE id = $1::uuid`, f.templateID); err != nil {
		t.Fatal(err)
	}
	r2, created, err := PublishIfChanged(ctx, f.tx, f.workspaceID, f.templateID, f.userID, "second")
	if err != nil || !created || r2.Revision != 2 {
		t.Fatalf("changed publish = %+v created=%v err=%v", r2, created, err)
	}
}

func TestApplyCopiesSharedConfigOnly(t *testing.T) {
	f := openTx(t)
	ctx := context.Background()
	if _, err := Create(ctx, f.tx, f.workspaceID, f.templateID, f.userID); err != nil {
		t.Fatal(err)
	}
	tenant, err := InsertTenant(ctx, f.tx, f.workspaceID, f.templateID, f.employeeID, "Think", f.userID)
	if err != nil {
		t.Fatal(err)
	}
	// A connector granted to the template that no longer exists at apply time.
	staleConnector := uuid.NewString()
	if _, err := f.tx.Exec(ctx, `INSERT INTO internal_connector_agent (connector_id, workspace_id, agent_id) VALUES ($1::uuid, $2::uuid, $3::uuid)`,
		staleConnector, f.workspaceID, f.templateID); err != nil {
		t.Fatal(err)
	}
	rev, _, err := PublishIfChanged(ctx, f.tx, f.workspaceID, f.templateID, f.userID, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Apply(ctx, f.tx, f.workspaceID, tenant, rev, f.userID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != rev.Revision || len(result.SkippedConnectorIDs) != 1 || result.SkippedConnectorIDs[0] != staleConnector {
		t.Fatalf("apply result = %+v", result)
	}

	var name, instructions, model, persona, permission string
	var coordinator bool
	var mcp json.RawMessage
	if err := f.tx.QueryRow(ctx, `SELECT name, instructions, model, persona, permission_mode, inbound_coordinator, mcp_config FROM agent WHERE id = $1::uuid`,
		f.employeeID).Scan(&name, &instructions, &model, &persona, &permission, &coordinator, &mcp); err != nil {
		t.Fatal(err)
	}
	if instructions != "shared instructions" || model != "model-a" || string(mcp) != `{"mcpServers": {}}` {
		t.Fatalf("shared config not applied: instructions=%q model=%q mcp=%s", instructions, model, mcp)
	}
	// Tenant-owned columns (persona, inbound coordinator) and identity stay
	// the employee's own across applies.
	if persona != "" || coordinator || name != "QwenTag · Think" || permission != "public_to" {
		t.Fatalf("apply touched tenant-owned columns: name=%q permission=%q persona=%q coordinator=%v", name, permission, persona, coordinator)
	}
	// Seeding is what gives a new employee the template's defaults.
	if err := SeedAgent(ctx, f.tx, f.workspaceID, f.templateID, f.employeeID); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.QueryRow(ctx, `SELECT name, persona, inbound_coordinator, instructions FROM agent WHERE id = $1::uuid`,
		f.employeeID).Scan(&name, &persona, &coordinator, &instructions); err != nil {
		t.Fatal(err)
	}
	if persona != "friendly" || !coordinator || name != "QwenTag · Think" || instructions != "shared instructions" {
		t.Fatalf("seed: name=%q persona=%q coordinator=%v instructions=%q", name, persona, coordinator, instructions)
	}
	if _, err := f.tx.Exec(ctx, `UPDATE agent SET persona = 'tenant voice' WHERE id = $1::uuid`, f.employeeID); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(ctx, f.tx, f.workspaceID, tenant, rev, f.userID); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.QueryRow(ctx, `SELECT persona FROM agent WHERE id = $1::uuid`, f.employeeID).Scan(&persona); err != nil {
		t.Fatal(err)
	}
	if persona != "tenant voice" {
		t.Fatalf("re-apply overwrote the tenant's persona: %q", persona)
	}
	var skills, connectors, offers int
	if err := f.tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM agent_skill WHERE agent_id = $1::uuid AND skill_id = $2::uuid AND enabled),
			(SELECT count(*) FROM internal_connector_agent WHERE agent_id = $1::uuid AND connector_id = $3::uuid),
			(SELECT count(*) FROM context_capability_binding WHERE agent_id = $1::uuid AND scope_type = 'offer' AND resource_id = $2::uuid)`,
		f.employeeID, f.skillID, f.connectorID).Scan(&skills, &connectors, &offers); err != nil {
		t.Fatal(err)
	}
	if skills != 1 || connectors != 1 || offers != 1 {
		t.Fatalf("collections not applied: skills=%d connectors=%d offers=%d", skills, connectors, offers)
	}
	applied, err := GetTenant(ctx, f.tx, f.workspaceID, tenant.ID)
	if err != nil || applied.AppliedRevision == nil || *applied.AppliedRevision != rev.Revision {
		t.Fatalf("applied tenant = %+v, %v", applied, err)
	}

	// Editing the template does not reach the tenant until the next apply.
	if _, err := f.tx.Exec(ctx, `UPDATE agent SET instructions = 'unpublished edit' WHERE id = $1::uuid`, f.templateID); err != nil {
		t.Fatal(err)
	}
	if err := f.tx.QueryRow(ctx, `SELECT instructions FROM agent WHERE id = $1::uuid`, f.employeeID).Scan(&instructions); err != nil {
		t.Fatal(err)
	}
	if instructions != "shared instructions" {
		t.Fatalf("unapplied template edit leaked to the employee: %q", instructions)
	}
}
