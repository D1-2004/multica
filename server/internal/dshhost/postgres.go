package dshhost

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Database interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// DB may be a pool or the connection already holding task admission locks.
type PostgresStore struct{ DB Database }

const columns = `workspace_id, agent_id, file_system_id, space_id, volume_name, access_point_arn, role_arn, vpc_id, security_group_id, vswitch_ids,
 state, generation, COALESCE(create_intent, '00000000-0000-0000-0000-000000000000'::uuid), sandbox_id, template_id, updated_at`

func readHost(row pgx.Row) (Host, error) {
	var h Host
	err := row.Scan(&h.WorkspaceID, &h.AgentID, &h.FileSystemID, &h.SpaceID, &h.VolumeName, &h.AccessPointARN,
		&h.RoleARN, &h.VPCID, &h.SecurityGroupID, &h.VSwitchIDs, &h.State, &h.Generation, &h.CreateIntent, &h.SandboxID, &h.TemplateID, &h.UpdatedAt)
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
	if key.WorkspaceID == uuid.Nil || key.AgentID == uuid.Nil || storage.FileSystemID == "" || storage.SpaceID == "" || storage.VolumeName == "" || storage.AccessPointARN == "" || storage.RoleARN == "" || storage.VPCID == "" || storage.SecurityGroupID == "" || len(storage.VSwitchIDs) == 0 {
		return Host{}, errors.New("incomplete DSH employee storage")
	}
	for _, id := range storage.VSwitchIDs {
		if id == "" {
			return Host{}, errors.New("empty DSH storage vSwitch")
		}
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO dsh_employee_host
 (workspace_id, agent_id, file_system_id, space_id, volume_name, access_point_arn, role_arn, vpc_id, security_group_id, vswitch_ids)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (workspace_id, agent_id) DO NOTHING`,
		key.WorkspaceID, key.AgentID, storage.FileSystemID, storage.SpaceID, storage.VolumeName, storage.AccessPointARN, storage.RoleARN, storage.VPCID, storage.SecurityGroupID, storage.VSwitchIDs)
	if err != nil {
		return Host{}, err
	}
	h, err := s.Get(ctx, key)
	if err != nil {
		return Host{}, err
	}
	if h.FileSystemID != storage.FileSystemID || h.SpaceID != storage.SpaceID || h.VolumeName != storage.VolumeName || h.AccessPointARN != storage.AccessPointARN || h.RoleARN != storage.RoleARN || h.VPCID != storage.VPCID || h.SecurityGroupID != storage.SecurityGroupID || !slices.Equal(h.VSwitchIDs, storage.VSwitchIDs) {
		return Host{}, fmt.Errorf("%w: employee storage is immutable", ErrChanged)
	}
	return h, nil
}

func (s PostgresStore) Get(ctx context.Context, key Key) (Host, error) {
	return readHost(s.DB.QueryRow(ctx, `SELECT `+columns+` FROM dsh_employee_host
 WHERE workspace_id=$1 AND agent_id=$2`, key.WorkspaceID, key.AgentID))
}

func (s PostgresStore) BeginCreate(ctx context.Context, key Key, generation int64, intent uuid.UUID, template string) (Host, error) {
	if intent == uuid.Nil || template == "" {
		return Host{}, errors.New("invalid DSH create intent")
	}
	return transition(s.DB.QueryRow(ctx, `UPDATE dsh_employee_host
 SET state='creating', generation=generation+1, create_intent=$4, template_id=$5, updated_at=now()
 WHERE workspace_id=$1 AND agent_id=$2 AND generation=$3 AND state='offline'
 RETURNING `+columns, key.WorkspaceID, key.AgentID, generation, intent, template))
}

func (s PostgresStore) CompleteCreate(ctx context.Context, h Host, sandboxID string) (Host, error) {
	return transition(s.DB.QueryRow(ctx, `UPDATE dsh_employee_host
 SET state='running', sandbox_id=$5, updated_at=now()
 WHERE workspace_id=$1 AND agent_id=$2 AND generation=$3 AND create_intent=$4 AND state='creating'
 RETURNING `+columns, h.WorkspaceID, h.AgentID, h.Generation, h.CreateIntent, sandboxID))
}

func (s PostgresStore) BeginRetire(ctx context.Context, h Host) (Host, error) {
	return transition(s.DB.QueryRow(ctx, `UPDATE dsh_employee_host
 SET state='retiring', updated_at=now()
 WHERE workspace_id=$1 AND agent_id=$2 AND generation=$3 AND sandbox_id=$4 AND state='running'
 RETURNING `+columns, h.WorkspaceID, h.AgentID, h.Generation, h.SandboxID))
}

func (s PostgresStore) CompleteRetire(ctx context.Context, h Host) error {
	result, err := s.DB.Exec(ctx, `UPDATE dsh_employee_host
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

func (s PostgresStore) AbandonCreate(ctx context.Context, h Host) error {
	result, err := s.DB.Exec(ctx, `UPDATE dsh_employee_host
 SET state='offline', sandbox_id='', create_intent=NULL, updated_at=now()
 WHERE workspace_id=$1 AND agent_id=$2 AND generation=$3 AND create_intent=$4 AND state='creating'`,
		h.WorkspaceID, h.AgentID, h.Generation, h.CreateIntent)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrChanged
	}
	return nil
}
