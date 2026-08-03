-- name: CreateWorkspaceAccessTokenSubject :one
INSERT INTO "user" (name, email, principal_type)
VALUES (
    sqlc.arg('name'),
    'workspace-access-token+' || gen_random_uuid()::text || '@internal.multica.invalid',
    'workspace_access_token'
)
RETURNING *;

-- name: CreateWorkspaceAccessToken :one
INSERT INTO workspace_access_token (
    workspace_id,
    subject_user_id,
    name,
    token_hash,
    token_prefix,
    expires_at,
    created_by,
    updated_by
)
VALUES (
    sqlc.arg('workspace_id'),
    sqlc.arg('subject_user_id'),
    sqlc.arg('name'),
    sqlc.arg('token_hash'),
    sqlc.arg('token_prefix'),
    sqlc.narg('expires_at'),
    sqlc.arg('actor_user_id'),
    sqlc.arg('actor_user_id')
)
RETURNING *;

-- name: GetWorkspaceAccessToken :one
SELECT * FROM workspace_access_token
WHERE id = sqlc.arg('id') AND workspace_id = sqlc.arg('workspace_id');

-- name: ListWorkspaceAccessTokens :many
SELECT * FROM workspace_access_token
WHERE workspace_id = sqlc.arg('workspace_id')
ORDER BY created_at DESC;

-- name: UpdateWorkspaceAccessToken :one
UPDATE workspace_access_token
SET name = sqlc.arg('name'),
    expires_at = sqlc.narg('expires_at'),
    version = version + 1,
    updated_by = sqlc.arg('actor_user_id'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND version = sqlc.arg('version')
  AND revoked_at IS NULL
RETURNING *;

-- name: RegenerateWorkspaceAccessToken :one
UPDATE workspace_access_token
SET token_hash = sqlc.arg('token_hash'),
    token_prefix = sqlc.arg('token_prefix'),
    expires_at = sqlc.narg('expires_at'),
    last_used_at = NULL,
    version = version + 1,
    updated_by = sqlc.arg('actor_user_id'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND version = sqlc.arg('version')
  AND revoked_at IS NULL
RETURNING *;

-- name: RevokeWorkspaceAccessToken :one
UPDATE workspace_access_token
SET revoked_at = now(),
    revoked_by = sqlc.arg('actor_user_id'),
    version = version + 1,
    updated_by = sqlc.arg('actor_user_id'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND revoked_at IS NULL
RETURNING *;

-- name: DeleteRevokedWorkspaceAccessToken :execrows
DELETE FROM workspace_access_token
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND revoked_at IS NOT NULL;

-- name: HasWorkspaceAccessTokenForSubject :one
SELECT EXISTS (
    SELECT 1
    FROM workspace_access_token
    WHERE workspace_id = sqlc.arg('workspace_id')
      AND subject_user_id = sqlc.arg('subject_user_id')
);

-- name: GetWorkspaceAccessTokenByHash :one
SELECT t.*, u.principal_type
FROM workspace_access_token t
JOIN "user" u ON u.id = t.subject_user_id
WHERE t.token_hash = sqlc.arg('token_hash');

-- name: UpdateWorkspaceAccessTokenLastUsed :exec
UPDATE workspace_access_token
SET last_used_at = now()
WHERE id = sqlc.arg('id')
  AND (last_used_at IS NULL OR last_used_at < now() - interval '5 minutes');

-- name: CreateWorkspaceAccessAudit :one
INSERT INTO workspace_access_audit (
    workspace_id,
    token_id,
    actor_user_id,
    action,
    resource_type,
    resource_id,
    result,
    request_id
)
VALUES (
    sqlc.arg('workspace_id'),
    sqlc.narg('token_id'),
    sqlc.narg('actor_user_id'),
    sqlc.arg('action'),
    sqlc.narg('resource_type'),
    sqlc.narg('resource_id'),
    sqlc.arg('result'),
    sqlc.narg('request_id')
)
RETURNING *;
