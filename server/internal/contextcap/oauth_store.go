package contextcap

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrLocked means another transaction holds the row lock a NOWAIT lock
// asked for (SQLSTATE 55P03, lock_not_available).
var ErrLocked = errors.New("contextcap: row is locked")

// IsLockNotAvailable reports whether err is Postgres' lock_not_available.
func IsLockNotAvailable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}

// LockCredential loads one scene or person credential with its ciphertext
// and locks the row until tx ends (SELECT ... FOR UPDATE NOWAIT). OAuth
// refresh takes this lock so concurrent replicas refresh a token once; it
// never waits for it (ErrLocked while another refresh holds it), so no
// pooled connection queues behind a provider's token request. ErrNotFound
// when the row is gone (for example disconnected meanwhile).
func LockCredential(ctx context.Context, tx DBTX, key CredentialBinding) (Credential, error) {
	key, err := key.normalized()
	if err != nil {
		return Credential{}, ErrInvalidInput
	}
	out := Credential{}
	err = tx.QueryRow(ctx, `SELECT workspace_id::text, agent_id::text, connector_id::text, scope_type, org_id, scope_key, ciphertext, hint, updated_at
		FROM context_connector_credential
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND connector_id = $3::uuid
		  AND scope_type = $4 AND org_id = $5 AND scope_key = $6
		FOR UPDATE NOWAIT`,
		key.WorkspaceID, key.AgentID, key.ConnectorID, key.ScopeType, key.OrgID, key.ScopeKey,
	).Scan(&out.WorkspaceID, &out.AgentID, &out.ConnectorID, &out.ScopeType, &out.OrgID, &out.ScopeKey, &out.Ciphertext, &out.Hint, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credential{}, ErrNotFound
	}
	if IsLockNotAvailable(err) {
		return Credential{}, ErrLocked
	}
	return out, err
}

// ReplaceCredentialSecret rewrites the ciphertext and hint of an existing
// credential (a refreshed OAuth token) without changing who configured it.
// It reports whether the row still existed.
func ReplaceCredentialSecret(ctx context.Context, tx DBTX, key CredentialBinding, ciphertext []byte, hint string) (bool, error) {
	key, err := key.normalized()
	if err != nil || len(ciphertext) == 0 {
		return false, ErrInvalidInput
	}
	tag, err := tx.Exec(ctx, `UPDATE context_connector_credential SET ciphertext = $7, hint = $8, updated_at = now()
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND connector_id = $3::uuid
		  AND scope_type = $4 AND org_id = $5 AND scope_key = $6`,
		key.WorkspaceID, key.AgentID, key.ConnectorID, key.ScopeType, key.OrgID, key.ScopeKey, ciphertext, hint)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ConnectorCredentials returns, with ciphertext and newest first, up to limit
// scene and person credentials of one connector across the workspace's
// agents. Tool discovery for an official app uses one when no workspace
// credential exists (tools/list only; nothing else is invoked).
func ConnectorCredentials(ctx context.Context, db DBTX, workspaceID, connectorID string, limit int) ([]Credential, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := db.Query(ctx, `SELECT workspace_id::text, agent_id::text, connector_id::text, scope_type, org_id, scope_key, ciphertext, hint, updated_at
		FROM context_connector_credential
		WHERE workspace_id = $1::uuid AND connector_id = $2::uuid
		ORDER BY updated_at DESC, id
		LIMIT $3`, workspaceID, connectorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Credential{}
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.WorkspaceID, &c.AgentID, &c.ConnectorID, &c.ScopeType, &c.OrgID, &c.ScopeKey, &c.Ciphertext, &c.Hint, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
