// Package tag implements the workspace Tag: a single multi-tenant digital
// employee per workspace.
//
// Domain model:
//
//   - Tag (workspace_tag): at most one per workspace. It points at a template
//     agent that holds the Tag's shared configuration (instructions, MCP,
//     model and runtime, skills, granted connectors, offers, DSH plugins and
//     agent-level behavior switches). The template never receives dispatches
//     itself.
//   - Revision (tag_config_revision): an immutable snapshot of the template's
//     shared configuration. A revision is published only when the template
//     differs from the latest one.
//   - Tenant (tag_tenant): one enterprise served by the Tag, embodied by
//     exactly one employee agent. The employee is an ordinary agent row: it
//     owns the tenant's DingTalk digital-employee binding, dispatch endpoint,
//     scenes and enterprise-level context. The tenant's enterprise (OrgId) is
//     therefore the employee's bound identity org, so tenant resolution at
//     dispatch time is the existing "which agent received it" routing and the
//     claim path needs no Tag awareness.
//   - Apply: copying a revision onto a tenant's employee agent. Shared
//     configuration only reaches a tenant through an explicit apply.
package tag

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is the subset of pgx shared by *pgxpool.Pool, *pgx.Conn and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var (
	// ErrNotFound means no matching Tag, tenant or revision exists.
	ErrNotFound = errors.New("tag: not found")
	// ErrAlreadyExists means the workspace already has a Tag.
	ErrAlreadyExists = errors.New("tag: workspace already has a tag")
	// ErrAgentUnavailable means an agent is missing, archived or belongs to
	// another workspace.
	ErrAgentUnavailable = errors.New("tag: agent is not available in this workspace")
	// ErrAgentInUse means the agent is already the template or a tenant
	// employee of the Tag.
	ErrAgentInUse = errors.New("tag: agent already belongs to the tag")
	// ErrHasTenants means the Tag still has tenants.
	ErrHasTenants = errors.New("tag: tag still has tenants")
	// ErrAgentServesSeveralOrgs means the agent already serves organizations
	// beyond its own DingTalk identity through contextcap tenants; a Tag
	// employee serves exactly one enterprise.
	ErrAgentServesSeveralOrgs = errors.New("tag: agent serves several organizations")
)

// Role tells how an agent participates in its workspace Tag.
type Role string

const (
	RoleNone     Role = ""
	RoleTemplate Role = "template"
	RoleEmployee Role = "employee"
)

// Tag is a workspace's Tag registration.
type Tag struct {
	WorkspaceID    string
	AgentID        string
	SidebarVisible bool
	CreatedBy      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Get returns the workspace's Tag or ErrNotFound.
func Get(ctx context.Context, db DBTX, workspaceID string) (Tag, error) {
	var t Tag
	err := db.QueryRow(ctx, `SELECT workspace_id::text, agent_id::text, sidebar_visible, created_by::text, created_at, updated_at
		FROM workspace_tag WHERE workspace_id = $1::uuid`, workspaceID).
		Scan(&t.WorkspaceID, &t.AgentID, &t.SidebarVisible, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tag{}, ErrNotFound
	}
	return t, err
}

// LockWorkspace serializes Tag provisioning and tenant changes of one
// workspace until the transaction ends.
func LockWorkspace(ctx context.Context, tx DBTX, workspaceID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('workspace_tag:' || $1::text, 0))`, workspaceID)
	return err
}

// Create registers agentID as the workspace's Tag template. The caller holds
// LockWorkspace; the primary key still rejects a second Tag.
func Create(ctx context.Context, tx DBTX, workspaceID, agentID, createdBy string) (Tag, error) {
	if _, err := Get(ctx, tx, workspaceID); err == nil {
		return Tag{}, ErrAlreadyExists
	} else if !errors.Is(err, ErrNotFound) {
		return Tag{}, err
	}
	var t Tag
	err := tx.QueryRow(ctx, `INSERT INTO workspace_tag (workspace_id, agent_id, created_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid)
		RETURNING workspace_id::text, agent_id::text, sidebar_visible, created_by::text, created_at, updated_at`,
		workspaceID, agentID, createdBy).
		Scan(&t.WorkspaceID, &t.AgentID, &t.SidebarVisible, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return Tag{}, ErrAlreadyExists
	}
	return t, err
}

// SetSidebarVisible toggles whether workspace members see the Tag entry.
func SetSidebarVisible(ctx context.Context, db DBTX, workspaceID string, visible bool) (Tag, error) {
	var t Tag
	err := db.QueryRow(ctx, `UPDATE workspace_tag SET sidebar_visible = $2, updated_at = now()
		WHERE workspace_id = $1::uuid
		RETURNING workspace_id::text, agent_id::text, sidebar_visible, created_by::text, created_at, updated_at`,
		workspaceID, visible).
		Scan(&t.WorkspaceID, &t.AgentID, &t.SidebarVisible, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tag{}, ErrNotFound
	}
	return t, err
}

// Delete unregisters the workspace's Tag and its revisions. The template
// agent stays as an ordinary agent. It refuses while tenants remain, so no
// employee is left pointing at a vanished Tag.
func Delete(ctx context.Context, tx DBTX, workspaceID string) error {
	t, err := Get(ctx, tx, workspaceID)
	if err != nil {
		return err
	}
	var tenants int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM tag_tenant WHERE workspace_id = $1::uuid AND tag_agent_id = $2::uuid`,
		workspaceID, t.AgentID).Scan(&tenants); err != nil {
		return err
	}
	if tenants > 0 {
		return ErrHasTenants
	}
	if _, err := tx.Exec(ctx, `DELETE FROM tag_config_revision WHERE workspace_id = $1::uuid AND tag_agent_id = $2::uuid`,
		workspaceID, t.AgentID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM workspace_tag WHERE workspace_id = $1::uuid`, workspaceID)
	return err
}

// AgentRole reports whether agentID is the workspace Tag's template, one of
// its tenant employees, or neither.
func AgentRole(ctx context.Context, db DBTX, workspaceID, agentID string) (Role, error) {
	var role string
	err := db.QueryRow(ctx, `SELECT CASE
			WHEN EXISTS (SELECT 1 FROM workspace_tag WHERE workspace_id = $1::uuid AND agent_id = $2::uuid) THEN 'template'
			WHEN EXISTS (SELECT 1 FROM tag_tenant WHERE workspace_id = $1::uuid AND employee_agent_id = $2::uuid) THEN 'employee'
			ELSE '' END`, workspaceID, agentID).Scan(&role)
	return Role(role), err
}
