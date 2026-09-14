package dshhost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const provisionColumns = `workspace_id, agent_id, spec, intent, step, state, resources`

func (s PostgresStore) GetProvision(ctx context.Context, key Key) (Provision, error) {
	return readProvision(s.DB.QueryRow(ctx, `SELECT `+provisionColumns+` FROM dsh_storage_provision WHERE workspace_id=$1 AND agent_id=$2`, key.WorkspaceID, key.AgentID))
}

func readProvision(row pgx.Row) (Provision, error) {
	var p Provision
	var spec []byte
	if err := row.Scan(&p.WorkspaceID, &p.AgentID, &spec, &p.Intent, &p.Step, &p.State, &p.Resources); err != nil {
		return p, err
	}
	err := json.Unmarshal(spec, &p.Spec)
	return p, err
}

func (s PostgresStore) BeginProvision(ctx context.Context, key Key, spec ProvisionSpec) (Provision, error) {
	if key.WorkspaceID == uuid.Nil || key.AgentID == uuid.Nil || !spec.valid() {
		return Provision{}, errors.New("invalid DSH storage provisioning intent")
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return Provision{}, err
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO dsh_storage_provision (workspace_id,agent_id,spec,intent)
 VALUES ($1,$2,$3,$4) ON CONFLICT (workspace_id,agent_id) DO NOTHING`, key.WorkspaceID, key.AgentID, raw, uuid.New())
	if err != nil {
		return Provision{}, err
	}
	p, err := readProvision(s.DB.QueryRow(ctx, `SELECT `+provisionColumns+` FROM dsh_storage_provision WHERE workspace_id=$1 AND agent_id=$2`, key.WorkspaceID, key.AgentID))
	if err == nil && !reflect.DeepEqual(p.Spec, spec) {
		return Provision{}, ErrChanged
	}
	return p, err
}

func (s PostgresStore) ClaimProvisionStep(ctx context.Context, p Provision) (Provision, error) {
	next, err := readProvision(s.DB.QueryRow(ctx, `UPDATE dsh_storage_provision SET state='creating',updated_at=now()
 WHERE workspace_id=$1 AND agent_id=$2 AND intent=$3 AND step=$4 AND state='planned' AND step<6
 RETURNING `+provisionColumns, p.WorkspaceID, p.AgentID, p.Intent, p.Step))
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrChanged
	}
	return next, err
}

func (s PostgresStore) CompleteProvisionStep(ctx context.Context, p Provision, id string) (Provision, error) {
	if id == "" || len(id) > 1024 {
		return Provision{}, errors.New("invalid DSH storage resource identity")
	}
	next, err := readProvision(s.DB.QueryRow(ctx, `UPDATE dsh_storage_provision
 SET resources=array_append(resources,$5),step=step+1,state='planned',updated_at=now()
 WHERE workspace_id=$1 AND agent_id=$2 AND intent=$3 AND step=$4 AND state='creating' AND step<6
 RETURNING `+provisionColumns, p.WorkspaceID, p.AgentID, p.Intent, p.Step, id))
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrChanged
	}
	return next, err
}

func (s PostgresStore) FinishProvision(ctx context.Context, p Provision, storage Storage) (Host, error) {
	if err := validateProvisionStorage(p, storage); err != nil {
		return Host{}, err
	}
	// Re-read durable identities before exposing storage. A completed binding
	// is immutable; if the final status write is lost, retrying is harmless.
	current, err := readProvision(s.DB.QueryRow(ctx, `SELECT `+provisionColumns+` FROM dsh_storage_provision WHERE workspace_id=$1 AND agent_id=$2 AND intent=$3`, p.WorkspaceID, p.AgentID, p.Intent))
	if err != nil {
		return Host{}, err
	}
	if !reflect.DeepEqual(current, p) {
		return Host{}, ErrChanged
	}
	h, err := s.BindStorage(ctx, p.Key, storage)
	if err != nil {
		return Host{}, err
	}
	result, err := s.DB.Exec(ctx, `UPDATE dsh_storage_provision SET state='complete',updated_at=now()
 WHERE workspace_id=$1 AND agent_id=$2 AND intent=$3 AND step=6 AND state IN ('planned','complete')`, p.WorkspaceID, p.AgentID, p.Intent)
	if err != nil {
		return Host{}, err
	}
	if result.RowsAffected() != 1 {
		return Host{}, ErrChanged
	}
	return h, nil
}
