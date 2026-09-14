package dshhost

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const nativeAccessColumns = `g.id, g.workspace_id, g.agent_id, g.user_id, g.generation, g.sandbox_id, g.kind, g.expires_at`
const nativeAccessRunningHost = `EXISTS (SELECT 1 FROM dsh_employee_host h WHERE
 h.workspace_id=g.workspace_id AND h.agent_id=g.agent_id AND h.generation=g.generation
 AND h.sandbox_id=g.sandbox_id AND h.state='running')`

func readNativeAccess(row pgx.Row) (NativeAccess, error) {
	var access NativeAccess
	err := row.Scan(&access.ID, &access.WorkspaceID, &access.AgentID, &access.UserID, &access.Generation, &access.SandboxID, &access.Kind, &access.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return NativeAccess{}, ErrNativeAccessDenied
	}
	return access, err
}

func (s PostgresStore) InsertNativeAccess(ctx context.Context, access NativeAccess, hash string) (NativeAccess, error) {
	return readNativeAccess(s.DB.QueryRow(ctx, `INSERT INTO dsh_native_access AS g
 (id,workspace_id,agent_id,user_id,generation,sandbox_id,kind,token_hash,expires_at)
 SELECT $1,$2,$3,$4,$5,$6,'entry',$7,clock_timestamp()+interval '1 minute'
 WHERE EXISTS (SELECT 1 FROM dsh_employee_host h WHERE h.workspace_id=$2 AND h.agent_id=$3
 AND h.generation=$5 AND h.sandbox_id=$6 AND h.state='running')
 RETURNING `+nativeAccessColumns, access.ID, access.WorkspaceID, access.AgentID, access.UserID, access.Generation, access.SandboxID, hash))
}

func (s PostgresStore) GetNativeAccess(ctx context.Context, hash, kind string) (NativeAccess, error) {
	if kind != "entry" && kind != "session" {
		return NativeAccess{}, ErrNativeAccessDenied
	}
	return readNativeAccess(s.DB.QueryRow(ctx, `SELECT `+nativeAccessColumns+` FROM dsh_native_access g
 WHERE g.token_hash=$1 AND g.kind=$2 AND g.expires_at>clock_timestamp() AND `+nativeAccessRunningHost, hash, kind))
}

func (s PostgresStore) ExchangeNativeAccess(ctx context.Context, access NativeAccess, entryHash, sessionHash string) (NativeAccess, error) {
	return readNativeAccess(s.DB.QueryRow(ctx, `UPDATE dsh_native_access g SET
 token_hash=$2, kind='session', expires_at=clock_timestamp()+interval '15 minutes', exchanged_at=clock_timestamp()
 WHERE g.token_hash=$1 AND g.id=$3 AND g.kind='entry' AND g.expires_at>clock_timestamp()
 AND `+nativeAccessRunningHost+` RETURNING `+nativeAccessColumns, entryHash, sessionHash, access.ID))
}

func (s PostgresStore) RevokeNativeAccess(ctx context.Context, key Key, grantID uuid.UUID) error {
	_, err := s.DB.Exec(ctx, `UPDATE dsh_native_access SET kind='revoked', revoked_at=clock_timestamp()
 WHERE workspace_id=$1 AND agent_id=$2 AND id=$3 AND kind<>'revoked'`, key.WorkspaceID, key.AgentID, grantID)
	return err
}
