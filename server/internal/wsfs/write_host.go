package wsfs

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// WriteHost is the single read-write sandbox for a workspace shared volume.
// Uploads go through it so read-only agent mounts can see the files.
type WriteHost struct {
	WorkspaceID  uuid.UUID
	State        string
	Generation   int64
	CreateIntent uuid.UUID
	SandboxID    string
	TemplateID   string
	VolumeName   string
	RoleARN      string
}

type WriteNetwork struct {
	VPCID           string
	SecurityGroupID string
	VSwitchIDs      []string
}

func (s Store) WriteNetwork(ctx context.Context, workspaceID uuid.UUID) (WriteNetwork, error) {
	var net WriteNetwork
	err := s.DB.QueryRow(ctx, `SELECT vpc_id, security_group_id, vswitch_ids
 FROM workspace_filesystem WHERE workspace_id=$1`, workspaceID).Scan(&net.VPCID, &net.SecurityGroupID, &net.VSwitchIDs)
	return net, err
}

func (s Store) GetWriteHost(ctx context.Context, workspaceID uuid.UUID) (WriteHost, error) {
	var h WriteHost
	err := s.DB.QueryRow(ctx, `SELECT workspace_id, state, generation,
 COALESCE(create_intent, '00000000-0000-0000-0000-000000000000'::uuid),
 sandbox_id, template_id, volume_name, role_arn
 FROM workspace_filesystem_host WHERE workspace_id=$1 AND mode='write'`, workspaceID).Scan(
		&h.WorkspaceID, &h.State, &h.Generation, &h.CreateIntent, &h.SandboxID, &h.TemplateID, &h.VolumeName, &h.RoleARN)
	if errors.Is(err, pgx.ErrNoRows) {
		return WriteHost{WorkspaceID: workspaceID, State: "offline"}, nil
	}
	return h, err
}

func (s Store) BeginWriteHost(ctx context.Context, workspaceID, intent uuid.UUID, generation int64, template, volume, role string) (WriteHost, error) {
	if intent == uuid.Nil || template == "" || volume == "" || role == "" || generation < 1 {
		return WriteHost{}, errors.New("invalid workspace write host intent")
	}
	var h WriteHost
	err := s.DB.QueryRow(ctx, `INSERT INTO workspace_filesystem_host
 (workspace_id, mode, state, generation, create_intent, sandbox_id, template_id, volume_name, role_arn)
 VALUES ($1,'write','creating',$2,$3,'',$4,$5,$6)
 ON CONFLICT (workspace_id, mode) DO UPDATE SET
  state='creating', generation=$2, create_intent=$3, sandbox_id='', template_id=$4, volume_name=$5, role_arn=$6, updated_at=now()
 WHERE workspace_filesystem_host.state='offline'
 RETURNING workspace_id, state, generation, create_intent, sandbox_id, template_id, volume_name, role_arn`,
		workspaceID, generation, intent, template, volume, role).Scan(
		&h.WorkspaceID, &h.State, &h.Generation, &h.CreateIntent, &h.SandboxID, &h.TemplateID, &h.VolumeName, &h.RoleARN)
	if errors.Is(err, pgx.ErrNoRows) {
		return WriteHost{}, errors.New("workspace write host is not offline")
	}
	return h, err
}

func (s Store) CompleteWriteHost(ctx context.Context, h WriteHost, sandboxID string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE workspace_filesystem_host
 SET state='running', sandbox_id=$4, updated_at=now()
 WHERE workspace_id=$1 AND mode='write' AND generation=$2 AND create_intent=$3 AND state='creating'`,
		h.WorkspaceID, h.Generation, h.CreateIntent, sandboxID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("workspace write host create intent changed")
	}
	return nil
}

func (s Store) ReleaseWriteHost(ctx context.Context, h WriteHost) error {
	tag, err := s.DB.Exec(ctx, `UPDATE workspace_filesystem_host
 SET state='offline', sandbox_id='', create_intent=NULL, updated_at=now()
 WHERE workspace_id=$1 AND mode='write' AND generation=$2 AND create_intent=$3 AND state IN ('creating','running','retiring')`,
		h.WorkspaceID, h.Generation, h.CreateIntent)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("workspace write host generation changed")
	}
	return nil
}
