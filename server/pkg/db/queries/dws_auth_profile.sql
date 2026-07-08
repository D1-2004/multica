-- name: CreateDWSAuthProfile :one
INSERT INTO dws_auth_profile (
    workspace_id, owner_id, label, corp_id, corp_name,
    user_id, user_name, auth_archive_encrypted
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: ListDWSAuthProfiles :many
SELECT * FROM dws_auth_profile
WHERE workspace_id = $1
  AND status = 'active'
ORDER BY updated_at DESC, created_at DESC;

-- name: GetDWSAuthProfileForWorkspace :one
SELECT * FROM dws_auth_profile
WHERE id = $1
  AND workspace_id = $2
  AND status = 'active';

-- name: RevokeDWSAuthProfile :one
UPDATE dws_auth_profile
SET status = 'revoked', updated_at = now()
WHERE id = $1
  AND workspace_id = $2
RETURNING *;
