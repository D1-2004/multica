package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

// WorkspaceMCPToken is deliberately separate from workspace_access_token:
// that credential has service-member owner elevation in request middleware.
type WorkspaceMCPToken struct {
	ID            pgtype.UUID
	WorkspaceID   pgtype.UUID
	SubjectUserID pgtype.UUID
	Scopes        []string
	ExpiresAt     pgtype.Timestamptz
	RevokedAt     pgtype.Timestamptz
	Role          string
}

func (q *Queries) GetWorkspaceMCPTokenByHash(ctx context.Context, hash string) (WorkspaceMCPToken, error) {
	var token WorkspaceMCPToken
	err := q.db.QueryRow(ctx, `SELECT t.id, t.workspace_id, t.subject_user_id, t.scopes,
		t.expires_at, t.revoked_at, m.role
		FROM workspace_mcp_token t
		JOIN member m ON m.workspace_id = t.workspace_id AND m.user_id = t.subject_user_id
		JOIN "user" u ON u.id = t.subject_user_id
		WHERE t.token_hash = $1`, hash).Scan(&token.ID, &token.WorkspaceID,
		&token.SubjectUserID, &token.Scopes, &token.ExpiresAt, &token.RevokedAt, &token.Role)
	return token, err
}

func (q *Queries) RecordWorkspaceMCPCall(ctx context.Context, token WorkspaceMCPToken, tool, resource, result string) error {
	_, err := q.db.Exec(ctx, `INSERT INTO workspace_mcp_audit
		(workspace_id, token_id, subject_user_id, actor_user_id, tool_name, resource_id, result)
		VALUES ($1, $2, $3, $3, $4, $5, $6)`, token.WorkspaceID, token.ID,
		token.SubjectUserID, tool, resource, result)
	return err
}

func (q *Queries) TouchWorkspaceMCPToken(ctx context.Context, id pgtype.UUID) error {
	_, err := q.db.Exec(ctx, `UPDATE workspace_mcp_token SET last_used_at = now()
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '5 minutes')`, id)
	return err
}
