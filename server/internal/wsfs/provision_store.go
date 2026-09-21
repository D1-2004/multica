package wsfs

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

const provisionColumns = `workspace_id, spec, intent, step, state, resources`

func (s Store) GetProvision(ctx context.Context, workspaceID uuid.UUID) (WorkspaceProvision, error) {
	return readWorkspaceProvision(s.DB.QueryRow(ctx, `SELECT `+provisionColumns+` FROM workspace_filesystem_provision WHERE workspace_id=$1`, workspaceID))
}

func readWorkspaceProvision(row pgx.Row) (WorkspaceProvision, error) {
	var p WorkspaceProvision
	var spec []byte
	if err := row.Scan(&p.WorkspaceID, &spec, &p.Intent, &p.Step, &p.State, &p.Resources); err != nil {
		return p, err
	}
	err := json.Unmarshal(spec, &p.Spec)
	return p, err
}

func (s Store) BeginProvision(ctx context.Context, workspaceID uuid.UUID, spec dshhost.ProvisionSpec) (WorkspaceProvision, error) {
	if workspaceID == uuid.Nil || spec.Validate() != nil {
		return WorkspaceProvision{}, errors.New("invalid workspace filesystem provisioning intent")
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return WorkspaceProvision{}, err
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO workspace_filesystem_provision (workspace_id, spec, intent)
 VALUES ($1,$2,$3) ON CONFLICT (workspace_id) DO NOTHING`, workspaceID, raw, uuid.New())
	if err != nil {
		return WorkspaceProvision{}, err
	}
	p, err := readWorkspaceProvision(s.DB.QueryRow(ctx, `SELECT `+provisionColumns+` FROM workspace_filesystem_provision WHERE workspace_id=$1`, workspaceID))
	if err == nil && !reflect.DeepEqual(p.Spec, spec) {
		return WorkspaceProvision{}, dshhost.ErrChanged
	}
	return p, err
}

func (s Store) ClaimProvisionStep(ctx context.Context, p WorkspaceProvision) (WorkspaceProvision, error) {
	next, err := readWorkspaceProvision(s.DB.QueryRow(ctx, `UPDATE workspace_filesystem_provision SET state='creating', updated_at=now()
 WHERE workspace_id=$1 AND intent=$2 AND step=$3 AND state='planned' AND step<11
 RETURNING `+provisionColumns, p.WorkspaceID, p.Intent, p.Step))
	if errors.Is(err, pgx.ErrNoRows) {
		err = dshhost.ErrChanged
	}
	return next, err
}

func (s Store) CompleteProvisionStep(ctx context.Context, p WorkspaceProvision, id string) (WorkspaceProvision, error) {
	if id == "" || len(id) > 1024 {
		return WorkspaceProvision{}, errors.New("invalid workspace filesystem resource identity")
	}
	next, err := readWorkspaceProvision(s.DB.QueryRow(ctx, `UPDATE workspace_filesystem_provision
 SET resources=array_append(resources,$4), step=step+1, state='planned', updated_at=now()
 WHERE workspace_id=$1 AND intent=$2 AND step=$3 AND state='creating' AND step<11
 RETURNING `+provisionColumns, p.WorkspaceID, p.Intent, p.Step, id))
	if errors.Is(err, pgx.ErrNoRows) {
		err = dshhost.ErrChanged
	}
	return next, err
}

func (s Store) FinishProvision(ctx context.Context, p WorkspaceProvision, binding Binding) (Binding, error) {
	if err := validateWorkspaceStorage(p, binding); err != nil {
		return Binding{}, err
	}
	current, err := readWorkspaceProvision(s.DB.QueryRow(ctx, `SELECT `+provisionColumns+` FROM workspace_filesystem_provision WHERE workspace_id=$1 AND intent=$2`, p.WorkspaceID, p.Intent))
	if err != nil {
		return Binding{}, err
	}
	if !reflect.DeepEqual(current, p) {
		return Binding{}, dshhost.ErrChanged
	}
	bound, err := s.BindStorage(ctx, p, binding)
	if err != nil {
		return Binding{}, err
	}
	result, err := s.DB.Exec(ctx, `UPDATE workspace_filesystem_provision SET state='complete', updated_at=now()
 WHERE workspace_id=$1 AND intent=$2 AND step=11 AND state IN ('planned','complete')`, p.WorkspaceID, p.Intent)
	if err != nil {
		return Binding{}, err
	}
	if result.RowsAffected() != 1 {
		return Binding{}, dshhost.ErrChanged
	}
	return bound, nil
}

func (s Store) BindStorage(ctx context.Context, p WorkspaceProvision, b Binding) (Binding, error) {
	if p.WorkspaceID == uuid.Nil || b.ROVolumeName == "" || b.RWVolumeName == "" || b.RORoleARN == "" || b.RWRoleARN == "" ||
		b.ROAccessPoint == "" || b.RWAccessPoint == "" {
		return Binding{}, errors.New("incomplete workspace filesystem storage")
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO workspace_filesystem
 (workspace_id, file_system_id, space_id, vpc_id, security_group_id, vswitch_ids,
  ro_access_point_arn, rw_access_point_arn, ro_role_arn, rw_role_arn, ro_volume_name, rw_volume_name,
  size_limit, file_count_limit)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
 ON CONFLICT (workspace_id) DO NOTHING`,
		p.WorkspaceID, p.Spec.FileSystemID, p.Resources[ProvisionSpace], p.Spec.VPCID, p.Spec.SecurityGroupID, p.Spec.VSwitchIDs,
		b.ROAccessPoint, b.RWAccessPoint, b.RORoleARN, b.RWRoleARN, b.ROVolumeName, b.RWVolumeName,
		p.Spec.SizeLimit, p.Spec.FileCountLimit)
	if err != nil {
		return Binding{}, err
	}
	got, err := s.GetBinding(ctx, p.WorkspaceID)
	if err != nil {
		return Binding{}, err
	}
	if got.ROVolumeName != b.ROVolumeName || got.RWVolumeName != b.RWVolumeName ||
		got.RORoleARN != b.RORoleARN || got.RWRoleARN != b.RWRoleARN ||
		got.ROAccessPoint != b.ROAccessPoint || got.RWAccessPoint != b.RWAccessPoint {
		return Binding{}, dshhost.ErrChanged
	}
	return got, nil
}
