package tag

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// MaxTenantNameLength mirrors tag_tenant_name_check.
const MaxTenantNameLength = 64

// ErrInvalidName means a tenant name is empty or too long.
var ErrInvalidName = errors.New("tag: tenant name must be 1-64 characters")

// Tenant is one enterprise served by the Tag together with its employee
// agent and that agent's DingTalk binding. OrgID is empty until the employee
// binds a digital employee.
type Tenant struct {
	ID              string
	WorkspaceID     string
	TagAgentID      string
	EmployeeAgentID string
	Name            string
	AppliedRevision *int32
	AppliedAt       *time.Time
	CreatedBy       string
	CreatedAt       time.Time

	EmployeeName     string
	EmployeeArchived bool
	// OrgID, OrganizationName and DigitalEmployeeName come from the
	// employee's agent_dingtalk_identity; empty when not yet bound.
	OrgID               string
	OrganizationName    string
	DigitalEmployeeName string
}

// Bound reports whether the employee has a DingTalk digital-employee
// identity, i.e. whether the tenant can receive messages.
func (t Tenant) Bound() bool { return t.OrgID != "" }

// NormalizeTenantName trims and validates a tenant display name.
func NormalizeTenantName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxTenantNameLength {
		return "", ErrInvalidName
	}
	return name, nil
}

const tenantSelect = `SELECT t.id::text, t.workspace_id::text, t.tag_agent_id::text, t.employee_agent_id::text, t.name,
		t.applied_revision, t.applied_at, t.created_by::text, t.created_at,
		COALESCE(e.name, ''), (e.id IS NULL OR e.archived_at IS NOT NULL),
		COALESCE(i.org_id, ''), COALESCE(i.organization_name, ''), COALESCE(i.account_display_name, '')
	FROM tag_tenant t
	LEFT JOIN agent e ON e.id = t.employee_agent_id AND e.workspace_id = t.workspace_id
	LEFT JOIN agent_dingtalk_identity i ON i.agent_id = t.employee_agent_id AND i.workspace_id = t.workspace_id`

func scanTenant(row pgx.Row) (Tenant, error) {
	var t Tenant
	err := row.Scan(&t.ID, &t.WorkspaceID, &t.TagAgentID, &t.EmployeeAgentID, &t.Name,
		&t.AppliedRevision, &t.AppliedAt, &t.CreatedBy, &t.CreatedAt,
		&t.EmployeeName, &t.EmployeeArchived,
		&t.OrgID, &t.OrganizationName, &t.DigitalEmployeeName)
	return t, err
}

// ListTenants returns the Tag's tenants in creation order.
func ListTenants(ctx context.Context, db DBTX, workspaceID, tagAgentID string) ([]Tenant, error) {
	rows, err := db.Query(ctx, tenantSelect+`
		WHERE t.workspace_id = $1::uuid AND t.tag_agent_id = $2::uuid
		ORDER BY t.created_at, t.id`, workspaceID, tagAgentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tenants := []Tenant{}
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		tenants = append(tenants, t)
	}
	return tenants, rows.Err()
}

// GetTenant returns one tenant of the workspace's Tag.
func GetTenant(ctx context.Context, db DBTX, workspaceID, tenantID string) (Tenant, error) {
	t, err := scanTenant(db.QueryRow(ctx, tenantSelect+`
		WHERE t.workspace_id = $1::uuid AND t.id = $2::uuid`, workspaceID, tenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Tenant{}, ErrNotFound
	}
	return t, err
}

// lockTenant locks a tenant row for an apply or rename.
func lockTenant(ctx context.Context, tx DBTX, workspaceID, tenantID string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM tag_tenant WHERE workspace_id = $1::uuid AND id = $2::uuid FOR UPDATE`,
		workspaceID, tenantID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// InsertTenant links employeeAgentID to the Tag as a new tenant. The employee
// must be a live agent of the workspace that is neither the template nor
// another tenant's employee, and serves no organization beyond its own
// identity. The caller holds LockWorkspace, so the Tag checked here cannot
// be removed before the caller commits.
func InsertTenant(ctx context.Context, tx DBTX, workspaceID, tagAgentID, employeeAgentID, name, createdBy string) (Tenant, error) {
	name, err := NormalizeTenantName(name)
	if err != nil {
		return Tenant{}, err
	}
	// The caller read the Tag before taking the workspace lock; it may have
	// been removed (or removed and recreated) since.
	current, err := Get(ctx, tx, workspaceID)
	if errors.Is(err, ErrNotFound) || (err == nil && current.AgentID != tagAgentID) {
		return Tenant{}, ErrTagChanged
	}
	if err != nil {
		return Tenant{}, err
	}
	if employeeAgentID == tagAgentID {
		return Tenant{}, ErrAgentInUse
	}
	if err := LockAgentTenancy(ctx, tx, employeeAgentID); err != nil {
		return Tenant{}, err
	}
	if err := requireLiveAgent(ctx, tx, workspaceID, employeeAgentID); err != nil {
		return Tenant{}, err
	}
	role, err := AgentRole(ctx, tx, workspaceID, employeeAgentID)
	if err != nil {
		return Tenant{}, err
	}
	if role != RoleNone {
		return Tenant{}, ErrAgentInUse
	}
	// One employee, one enterprise: an agent whose contextcap tenants reach
	// another org than its own identity cannot become a Tag tenant. A
	// tenant row for the identity org itself only renames it.
	var multiOrg bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM agent_tenant t
			LEFT JOIN agent_dingtalk_identity i ON i.agent_id = t.agent_id
			WHERE t.workspace_id = $1::uuid AND t.agent_id = $2::uuid
			  AND t.org_id IS DISTINCT FROM i.org_id)`, workspaceID, employeeAgentID).Scan(&multiOrg); err != nil {
		return Tenant{}, err
	}
	if multiOrg {
		return Tenant{}, ErrAgentServesSeveralOrgs
	}
	// What remains is at most an alias row naming the identity org. The Tag
	// tenant's name replaces it, and a Tag employee holds no contextcap
	// tenant rows at all: a row kept here would survive a later rebind to
	// another org as a second enterprise.
	if _, err := tx.Exec(ctx, `DELETE FROM agent_tenant WHERE workspace_id = $1::uuid AND agent_id = $2::uuid`,
		workspaceID, employeeAgentID); err != nil {
		return Tenant{}, err
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO tag_tenant (workspace_id, tag_agent_id, employee_agent_id, name, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::uuid) RETURNING id::text`,
		workspaceID, tagAgentID, employeeAgentID, name, createdBy).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return Tenant{}, ErrAgentInUse
	}
	if err != nil {
		return Tenant{}, err
	}
	return GetTenant(ctx, tx, workspaceID, id)
}

// RenameTenant changes a tenant's display name.
func RenameTenant(ctx context.Context, db DBTX, workspaceID, tenantID, name string) (Tenant, error) {
	name, err := NormalizeTenantName(name)
	if err != nil {
		return Tenant{}, err
	}
	tag, err := db.Exec(ctx, `UPDATE tag_tenant SET name = $3, updated_at = now()
		WHERE workspace_id = $1::uuid AND id = $2::uuid`, workspaceID, tenantID, name)
	if err != nil {
		return Tenant{}, err
	}
	if tag.RowsAffected() == 0 {
		return Tenant{}, ErrNotFound
	}
	return GetTenant(ctx, db, workspaceID, tenantID)
}

// DeleteTenant detaches a tenant. Its employee agent, binding and scenes stay
// as an ordinary agent; only the Tag link is removed.
func DeleteTenant(ctx context.Context, db DBTX, workspaceID, tenantID string) error {
	tag, err := db.Exec(ctx, `DELETE FROM tag_tenant WHERE workspace_id = $1::uuid AND id = $2::uuid`, workspaceID, tenantID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func requireLiveAgent(ctx context.Context, db DBTX, workspaceID, agentID string) error {
	var ok bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM agent
		WHERE id = $2::uuid AND workspace_id = $1::uuid AND archived_at IS NULL AND kind = 'user')`,
		workspaceID, agentID).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrAgentUnavailable
	}
	return nil
}
