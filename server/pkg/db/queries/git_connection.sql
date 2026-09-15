-- name: ListGitConnections :many
SELECT * FROM git_connection WHERE workspace_id = $1 ORDER BY created_at, id;

-- name: GetGitConnection :one
SELECT * FROM git_connection WHERE id = $1 AND workspace_id = $2;

-- name: CreateCodeGitConnection :one
INSERT INTO git_connection (workspace_id, provider, account_login, token_ciphertext, created_by)
VALUES ($1, 'alibaba_code', $2, $3, $4) RETURNING *;

-- name: UpdateCodeGitConnection :one
UPDATE git_connection SET account_login = $3, token_ciphertext = $4, updated_at = now()
WHERE id = $1 AND workspace_id = $2 AND provider = 'alibaba_code' RETURNING *;

-- name: DeleteCodeGitConnection :exec
WITH removed AS (
    DELETE FROM git_connection WHERE git_connection.id = $1 AND git_connection.workspace_id = $2 AND git_connection.provider = 'alibaba_code' RETURNING git_connection.id
)
UPDATE agent_source SET sync_status = 'disconnected', last_sync_error = 'Git connection disconnected', updated_at = now()
WHERE git_connection_id IN (SELECT id FROM removed);
