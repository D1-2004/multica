package dshhost

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const nativeAccessColumns = `g.id, g.workspace_id, g.agent_id, g.user_id, g.generation, g.sandbox_id, g.kind, g.expires_at, COALESCE(g.parent_access_id, '00000000-0000-0000-0000-000000000000'::uuid)`
const nativeAccessRunningHost = `EXISTS (SELECT 1 FROM employee_filesystem_host h WHERE
 h.workspace_id=g.workspace_id AND h.agent_id=g.agent_id AND h.generation=g.generation
 AND h.sandbox_id=g.sandbox_id AND h.state='running')`

// Routed grants remain subordinate to a live, same-human browser grant.
const nativeAccessParent = `(g.parent_access_id IS NULL OR EXISTS (SELECT 1 FROM dsh_native_access p
 WHERE p.id=g.parent_access_id AND p.parent_access_id IS NULL AND p.kind='session'
 AND p.workspace_id=g.workspace_id AND p.agent_id=g.agent_id AND p.user_id=g.user_id
 AND p.expires_at>clock_timestamp() AND EXISTS (SELECT 1 FROM employee_filesystem_host ph
 WHERE ph.workspace_id=p.workspace_id AND ph.agent_id=p.agent_id AND ph.sandbox_id=p.sandbox_id
 AND ph.generation=p.generation AND ph.state='running')))`

func readNativeAccess(row pgx.Row) (NativeAccess, error) {
	var access NativeAccess
	err := row.Scan(&access.ID, &access.WorkspaceID, &access.AgentID, &access.UserID, &access.Generation, &access.SandboxID, &access.Kind, &access.ExpiresAt, &access.ParentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return NativeAccess{}, ErrNativeAccessDenied
	}
	return access, err
}

func (s PostgresStore) InsertNativeAccess(ctx context.Context, access NativeAccess, hash string) (NativeAccess, error) {
	if access.ParentID != uuid.Nil {
		return readNativeAccess(s.DB.QueryRow(ctx, `INSERT INTO dsh_native_access AS g
 (id,workspace_id,agent_id,user_id,generation,sandbox_id,kind,token_hash,expires_at,parent_access_id)
 SELECT $1,$2,$3,$4,$5,$6,'entry',$7,LEAST(clock_timestamp()+interval '1 minute',p.expires_at),p.id
 FROM dsh_native_access p WHERE p.id=$8 AND p.parent_access_id IS NULL AND p.kind='session'
 AND p.workspace_id=$2 AND p.agent_id=$3 AND p.user_id=$4 AND p.expires_at>clock_timestamp()
 AND EXISTS (SELECT 1 FROM employee_filesystem_host h WHERE h.workspace_id=$2 AND h.agent_id=$3
 AND h.generation=$5 AND h.sandbox_id=$6 AND h.state='running')
 RETURNING `+nativeAccessColumns, access.ID, access.WorkspaceID, access.AgentID, access.UserID, access.Generation, access.SandboxID, hash, access.ParentID))
	}
	return readNativeAccess(s.DB.QueryRow(ctx, `INSERT INTO dsh_native_access AS g
 (id,workspace_id,agent_id,user_id,generation,sandbox_id,kind,token_hash,expires_at)
 SELECT $1,$2,$3,$4,$5,$6,'entry',$7,clock_timestamp()+interval '1 minute'
 WHERE EXISTS (SELECT 1 FROM employee_filesystem_host h WHERE h.workspace_id=$2 AND h.agent_id=$3
 AND h.generation=$5 AND h.sandbox_id=$6 AND h.state='running')
 RETURNING `+nativeAccessColumns, access.ID, access.WorkspaceID, access.AgentID, access.UserID, access.Generation, access.SandboxID, hash))
}

func (s PostgresStore) GetNativeAccess(ctx context.Context, hash, kind string) (NativeAccess, error) {
	if kind != "entry" && kind != "session" {
		return NativeAccess{}, ErrNativeAccessDenied
	}
	return readNativeAccess(s.DB.QueryRow(ctx, `SELECT `+nativeAccessColumns+` FROM dsh_native_access g
 WHERE g.token_hash=$1 AND g.kind=$2 AND g.expires_at>clock_timestamp() AND `+nativeAccessRunningHost+` AND `+nativeAccessParent, hash, kind))
}

// LookupNativeAccess locates a proxy upstream, never authorizes an operation.
// The sandbox gateway separately requires the entry token or session cookie.
func (s PostgresStore) LookupNativeAccess(ctx context.Context, id uuid.UUID) (NativeAccess, error) {
	return readNativeAccess(s.DB.QueryRow(ctx, `SELECT `+nativeAccessColumns+` FROM dsh_native_access g
 WHERE g.id=$1 AND g.kind IN ('entry','session') AND g.expires_at>clock_timestamp() AND `+nativeAccessRunningHost+` AND `+nativeAccessParent, id))
}

func (s PostgresStore) ExchangeNativeAccess(ctx context.Context, access NativeAccess, entryHash, sessionHash string) (NativeAccess, error) {
	return readNativeAccess(s.DB.QueryRow(ctx, `UPDATE dsh_native_access g SET
 token_hash=$2, kind='session', expires_at=LEAST(clock_timestamp()+interval '15 minutes',COALESCE((SELECT p.expires_at FROM dsh_native_access p WHERE p.id=g.parent_access_id),'infinity'::timestamptz)), exchanged_at=clock_timestamp()
 WHERE g.token_hash=$1 AND g.id=$3 AND g.kind='entry' AND g.expires_at>clock_timestamp()
 AND `+nativeAccessRunningHost+` AND `+nativeAccessParent+` RETURNING `+nativeAccessColumns, entryHash, sessionHash, access.ID))
}

func (s PostgresStore) RevokeNativeAccess(ctx context.Context, key Key, grantID uuid.UUID) error {
	_, err := s.DB.Exec(ctx, `UPDATE dsh_native_access SET kind='revoked', revoked_at=clock_timestamp()
 WHERE workspace_id=$1 AND agent_id=$2 AND id=$3 AND kind<>'revoked'`, key.WorkspaceID, key.AgentID, grantID)
	return err
}
