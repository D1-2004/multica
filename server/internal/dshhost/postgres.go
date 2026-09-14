package dshhost

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct{ Pool *pgxpool.Pool }

const columns = `workspace_id, agent_id, volume_name, access_point_arn, role_arn,
 state, generation, COALESCE(create_intent, '00000000-0000-0000-0000-000000000000'::uuid), sandbox_id, template_id`

func readHost(row pgx.Row) (Host, error) {
	var h Host
	err := row.Scan(&h.WorkspaceID, &h.AgentID, &h.VolumeName, &h.AccessPointARN,
		&h.RoleARN, &h.State, &h.Generation, &h.CreateIntent, &h.SandboxID, &h.TemplateID)
	return h, err
}

func transition(row pgx.Row) (Host, error) {
	h, err := readHost(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Host{}, ErrChanged
	}
	return h, err
}

// BindStorage records already provisioned, verified employee storage. It is
// immutable: retries cannot redirect an existing Home to a different volume.
// The caller must verify workspace membership and provider ownership first.
func (s PostgresStore) BindStorage(ctx context.Context, key Key, storage Storage) (Host, error) {
	if key.WorkspaceID == uuid.Nil || key.AgentID == uuid.Nil || storage.VolumeName == "" || storage.AccessPointARN == "" || storage.RoleARN == "" {
		return Host{}, errors.New("incomplete DSH employee storage")
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO dsh_employee_host
 (workspace_id, agent_id, volume_name, access_point_arn, role_arn)
 VALUES ($1,$2,$3,$4,$5) ON CONFLICT (workspace_id, agent_id) DO NOTHING`,
		key.WorkspaceID, key.AgentID, storage.VolumeName, storage.AccessPointARN, storage.RoleARN)
	if err != nil {
		return Host{}, err
	}
	h, err := s.Get(ctx, key)
	if err != nil {
		return Host{}, err
	}
	if h.Storage != storage {
		return Host{}, fmt.Errorf("%w: employee storage is immutable", ErrChanged)
	}
	return h, nil
}

func (s PostgresStore) Get(ctx context.Context, key Key) (Host, error) {
	return readHost(s.Pool.QueryRow(ctx, `SELECT `+columns+` FROM dsh_employee_host
 WHERE workspace_id=$1 AND agent_id=$2`, key.WorkspaceID, key.AgentID))
}

func (s PostgresStore) BeginCreate(ctx context.Context, key Key, generation int64, intent uuid.UUID, template string) (Host, error) {
	if intent == uuid.Nil || template == "" {
		return Host{}, errors.New("invalid DSH create intent")
	}
	return transition(s.Pool.QueryRow(ctx, `UPDATE dsh_employee_host
 SET state='creating', generation=generation+1, create_intent=$4, template_id=$5, updated_at=now()
 WHERE workspace_id=$1 AND agent_id=$2 AND generation=$3 AND state='offline'
 RETURNING `+columns, key.WorkspaceID, key.AgentID, generation, intent, template))
}

func (s PostgresStore) CompleteCreate(ctx context.Context, h Host, sandboxID string) (Host, error) {
	return transition(s.Pool.QueryRow(ctx, `UPDATE dsh_employee_host
 SET state='running', sandbox_id=$5, updated_at=now()
 WHERE workspace_id=$1 AND agent_id=$2 AND generation=$3 AND create_intent=$4 AND state='creating'
 RETURNING `+columns, h.WorkspaceID, h.AgentID, h.Generation, h.CreateIntent, sandboxID))
}

func (s PostgresStore) BeginRetire(ctx context.Context, h Host) (Host, error) {
	return transition(s.Pool.QueryRow(ctx, `UPDATE dsh_employee_host
 SET state='retiring', updated_at=now()
 WHERE workspace_id=$1 AND agent_id=$2 AND generation=$3 AND sandbox_id=$4 AND state='running'
 RETURNING `+columns, h.WorkspaceID, h.AgentID, h.Generation, h.SandboxID))
}

func (s PostgresStore) CompleteRetire(ctx context.Context, h Host) error {
	result, err := s.Pool.Exec(ctx, `UPDATE dsh_employee_host
 SET state='offline', sandbox_id='', create_intent=NULL, updated_at=now()
 WHERE workspace_id=$1 AND agent_id=$2 AND generation=$3 AND sandbox_id=$4 AND state='retiring'`,
		h.WorkspaceID, h.AgentID, h.Generation, h.SandboxID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrChanged
	}
	return nil
}
