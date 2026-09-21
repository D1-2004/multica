package wsfs

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Database interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Store struct{ DB Database }

func (s Store) GetBinding(ctx context.Context, workspaceID uuid.UUID) (Binding, error) {
	var b Binding
	err := s.DB.QueryRow(ctx, `SELECT workspace_id, ro_volume_name, rw_volume_name, ro_role_arn, rw_role_arn, ro_access_point_arn, rw_access_point_arn
 FROM workspace_filesystem WHERE workspace_id=$1`, workspaceID).Scan(
		&b.WorkspaceID, &b.ROVolumeName, &b.RWVolumeName, &b.RORoleARN, &b.RWRoleARN, &b.ROAccessPoint, &b.RWAccessPoint)
	return b, err
}

func (s Store) GetGrant(ctx context.Context, workspaceID, agentID uuid.UUID) (Grant, error) {
	var g Grant
	err := s.DB.QueryRow(ctx, `SELECT workspace_id, agent_id, access, generation, task_role_arn
 FROM workspace_filesystem_grant WHERE workspace_id=$1 AND agent_id=$2`, workspaceID, agentID).Scan(
		&g.WorkspaceID, &g.AgentID, &g.Access, &g.Generation, &g.TaskRoleARN)
	if errors.Is(err, pgx.ErrNoRows) {
		return Grant{WorkspaceID: workspaceID, AgentID: agentID, Access: AccessNone}, nil
	}
	return g, err
}

func (s Store) ListGrants(ctx context.Context, workspaceID uuid.UUID) ([]Grant, error) {
	rows, err := s.DB.Query(ctx, `SELECT workspace_id, agent_id, access, generation, task_role_arn
 FROM workspace_filesystem_grant WHERE workspace_id=$1 ORDER BY agent_id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.WorkspaceID, &g.AgentID, &g.Access, &g.Generation, &g.TaskRoleARN); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s Store) PutGrant(ctx context.Context, workspaceID, agentID, updatedBy uuid.UUID, access string) (Grant, error) {
	if access != AccessNone && access != AccessRead && access != AccessWrite {
		return Grant{}, errors.New("invalid filesystem grant")
	}
	var g Grant
	err := s.DB.QueryRow(ctx, `INSERT INTO workspace_filesystem_grant (workspace_id, agent_id, access, generation, updated_by)
 VALUES ($1,$2,$3, CASE WHEN $3='none' THEN 0 ELSE 1 END, $4)
 ON CONFLICT (workspace_id, agent_id) DO UPDATE SET
  access=EXCLUDED.access,
  generation=CASE
    WHEN workspace_filesystem_grant.access IS DISTINCT FROM EXCLUDED.access
      THEN workspace_filesystem_grant.generation+1
    ELSE workspace_filesystem_grant.generation
  END,
  task_role_arn=CASE
    WHEN workspace_filesystem_grant.access IS DISTINCT FROM EXCLUDED.access THEN ''
    ELSE workspace_filesystem_grant.task_role_arn
  END,
  updated_by=EXCLUDED.updated_by,
  updated_at=now()
 RETURNING workspace_id, agent_id, access, generation, task_role_arn`, workspaceID, agentID, access, updatedBy).Scan(
		&g.WorkspaceID, &g.AgentID, &g.Access, &g.Generation, &g.TaskRoleARN)
	return g, err
}

func (s Store) ListEmployeeFilesystemAgentIDs(ctx context.Context, workspaceID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.DB.Query(ctx, `SELECT agent_id FROM dsh_employee_host WHERE workspace_id=$1 ORDER BY agent_id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s Store) DeleteWorkspaceRows(ctx context.Context, workspaceID uuid.UUID) error {
	for _, q := range []string{
		`DELETE FROM workspace_filesystem_host WHERE workspace_id=$1`,
		`DELETE FROM workspace_filesystem_grant_role WHERE workspace_id=$1`,
		`DELETE FROM workspace_filesystem_grant WHERE workspace_id=$1`,
		`DELETE FROM workspace_filesystem WHERE workspace_id=$1`,
		`DELETE FROM workspace_filesystem_provision WHERE workspace_id=$1`,
	} {
		if _, err := s.DB.Exec(ctx, q, workspaceID); err != nil {
			return err
		}
	}
	return nil
}
