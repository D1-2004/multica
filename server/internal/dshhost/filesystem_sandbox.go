package dshhost

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// FilesystemSandboxStore coordinates one execution scope, not the entire
// employee filesystem. Each scope mounts the same immutable storage binding.
// An uncertain create or retirement only blocks replacement of that scope.
type FilesystemSandboxStore struct {
	DB      Database
	ScopeID uuid.UUID
}

const filesystemSandboxColumns = `e.workspace_id, e.agent_id, e.file_system_id, e.space_id, e.volume_name, e.access_point_arn, e.role_arn, e.vpc_id, e.security_group_id, e.vswitch_ids,
 h.state, h.generation, COALESCE(h.create_intent, '00000000-0000-0000-0000-000000000000'::uuid), h.sandbox_id, h.template_id, h.updated_at`
const filesystemSandboxJoin = ` JOIN dsh_employee_host e ON e.workspace_id=h.workspace_id AND e.agent_id=h.agent_id`

// LockRunningHost must run inside the caller's transaction. It locks the actual
// row, rather than the union view, to order grants/receipts against retirement.
func LockRunningHost(ctx context.Context, db Database, h Host) error {
	var found bool
	for _, table := range []string{"dsh_employee_host", "employee_filesystem_sandbox"} {
		err := db.QueryRow(ctx, `SELECT true FROM `+table+` WHERE workspace_id=$1 AND agent_id=$2
 AND state='running' AND generation=$3 AND sandbox_id=$4 AND ($5='' OR template_id=$5) FOR SHARE`, h.WorkspaceID, h.AgentID, h.Generation, h.SandboxID, h.TemplateID).Scan(&found)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		return err
	}
	return ErrChanged
}

func (s FilesystemSandboxStore) read(row pgx.Row) (Host, error) {
	h, err := readHost(row)
	h.ScopeID = s.ScopeID
	return h, err
}

func (s FilesystemSandboxStore) changed(row pgx.Row) (Host, error) {
	h, err := s.read(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Host{}, ErrChanged
	}
	return h, err
}

// Bind only creates a scope when an employee's storage already exists. Storage
// identity is never copied into a mutable session row or supplied by a client.
func (s FilesystemSandboxStore) Bind(ctx context.Context, key Key) (Host, error) {
	if s.ScopeID == uuid.Nil || key.WorkspaceID == uuid.Nil || key.AgentID == uuid.Nil {
		return Host{}, errors.New("invalid employee filesystem sandbox scope")
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO employee_filesystem_sandbox (workspace_id,agent_id,scope_id)
 SELECT workspace_id,agent_id,$3 FROM dsh_employee_host WHERE workspace_id=$1 AND agent_id=$2
 ON CONFLICT (workspace_id,agent_id,scope_id) DO NOTHING`, key.WorkspaceID, key.AgentID, s.ScopeID)
	if err != nil {
		return Host{}, err
	}
	return s.Get(ctx, key)
}

func (s FilesystemSandboxStore) Get(ctx context.Context, key Key) (Host, error) {
	return s.read(s.DB.QueryRow(ctx, `SELECT `+filesystemSandboxColumns+` FROM employee_filesystem_sandbox h`+filesystemSandboxJoin+`
 WHERE h.workspace_id=$1 AND h.agent_id=$2 AND h.scope_id=$3`, key.WorkspaceID, key.AgentID, s.ScopeID))
}

func (s FilesystemSandboxStore) BeginCreate(ctx context.Context, key Key, generation int64, intent uuid.UUID, template string) (Host, error) {
	if intent == uuid.Nil || template == "" || s.ScopeID == uuid.Nil {
		return Host{}, errors.New("invalid employee filesystem create intent")
	}
	return s.changed(s.DB.QueryRow(ctx, `WITH changed AS (UPDATE employee_filesystem_sandbox
 SET state='creating',generation=generation+1,create_intent=$5,template_id=$6,updated_at=now()
 WHERE workspace_id=$1 AND agent_id=$2 AND scope_id=$3 AND generation=$4 AND state='offline' RETURNING *)
 SELECT `+filesystemSandboxColumns+` FROM changed h`+filesystemSandboxJoin, key.WorkspaceID, key.AgentID, s.ScopeID, generation, intent, template))
}

func (s FilesystemSandboxStore) CompleteCreate(ctx context.Context, h Host, sandboxID string) (Host, error) {
	if h.ScopeID != s.ScopeID || sandboxID == "" {
		return Host{}, ErrChanged
	}
	return s.changed(s.DB.QueryRow(ctx, `WITH changed AS (UPDATE employee_filesystem_sandbox
 SET state='running',sandbox_id=$6,updated_at=now()
 WHERE workspace_id=$1 AND agent_id=$2 AND scope_id=$3 AND generation=$4 AND create_intent=$5 AND state='creating' RETURNING *)
 SELECT `+filesystemSandboxColumns+` FROM changed h`+filesystemSandboxJoin, h.WorkspaceID, h.AgentID, s.ScopeID, h.Generation, h.CreateIntent, sandboxID))
}

func (s FilesystemSandboxStore) BeginRetire(ctx context.Context, h Host) (Host, error) {
	if h.ScopeID != s.ScopeID {
		return Host{}, ErrChanged
	}
	return s.changed(s.DB.QueryRow(ctx, `WITH changed AS (UPDATE employee_filesystem_sandbox
 SET state='retiring',updated_at=now()
 WHERE workspace_id=$1 AND agent_id=$2 AND scope_id=$3 AND generation=$4 AND sandbox_id=$5 AND state='running' RETURNING *)
 SELECT `+filesystemSandboxColumns+` FROM changed h`+filesystemSandboxJoin, h.WorkspaceID, h.AgentID, s.ScopeID, h.Generation, h.SandboxID))
}

func (s FilesystemSandboxStore) AbortRetire(ctx context.Context, h Host) error {
	if h.ScopeID != s.ScopeID {
		return ErrChanged
	}
	result, err := s.DB.Exec(ctx, `UPDATE employee_filesystem_sandbox SET state='running',updated_at=now()
 WHERE workspace_id=$1 AND agent_id=$2 AND scope_id=$3 AND generation=$4 AND sandbox_id=$5 AND state='retiring'`,
		h.WorkspaceID, h.AgentID, s.ScopeID, h.Generation, h.SandboxID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrChanged
	}
	return nil
}

func (s FilesystemSandboxStore) CompleteRetire(ctx context.Context, h Host) error {
	if h.ScopeID != s.ScopeID {
		return ErrChanged
	}
	result, err := s.DB.Exec(ctx, `UPDATE employee_filesystem_sandbox SET state='offline',sandbox_id='',create_intent=NULL,updated_at=now()
 WHERE workspace_id=$1 AND agent_id=$2 AND scope_id=$3 AND generation=$4 AND sandbox_id=$5 AND state='retiring'`, h.WorkspaceID, h.AgentID, s.ScopeID, h.Generation, h.SandboxID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrChanged
	}
	return nil
}

func (s FilesystemSandboxStore) AbandonCreate(ctx context.Context, h Host) error {
	if h.ScopeID != s.ScopeID {
		return ErrChanged
	}
	result, err := s.DB.Exec(ctx, `UPDATE employee_filesystem_sandbox SET state='offline',sandbox_id='',create_intent=NULL,updated_at=now()
 WHERE workspace_id=$1 AND agent_id=$2 AND scope_id=$3 AND generation=$4 AND create_intent=$5 AND state='creating'`,
		h.WorkspaceID, h.AgentID, s.ScopeID, h.Generation, h.CreateIntent)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrChanged
	}
	return nil
}
