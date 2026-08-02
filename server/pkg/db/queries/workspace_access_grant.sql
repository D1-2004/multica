-- name: CreateWorkspaceAccessGrantSubject :one
INSERT INTO "user" (name, email, principal_type)
VALUES (
    sqlc.arg('name'),
    'workspace-access-grant+' || gen_random_uuid()::text || '@internal.multica.invalid',
    'workspace_access_grant'
)
RETURNING *;

-- name: CreateWorkspaceAccessGrant :one
INSERT INTO workspace_access_grant (
    workspace_id,
    subject_user_id,
    name,
    capabilities,
    resource_scope,
    created_by,
    updated_by
)
VALUES (
    sqlc.arg('workspace_id'),
    sqlc.arg('subject_user_id'),
    sqlc.arg('name'),
    sqlc.arg('capabilities'),
    sqlc.arg('resource_scope'),
    sqlc.arg('actor_user_id'),
    sqlc.arg('actor_user_id')
)
RETURNING *;

-- name: GetWorkspaceAccessGrant :one
SELECT * FROM workspace_access_grant
WHERE id = sqlc.arg('id') AND workspace_id = sqlc.arg('workspace_id');

-- name: ListWorkspaceAccessGrants :many
SELECT * FROM workspace_access_grant
WHERE workspace_id = sqlc.arg('workspace_id')
ORDER BY created_at DESC;

-- name: UpdateWorkspaceAccessGrant :one
UPDATE workspace_access_grant
SET name = sqlc.arg('name'),
    capabilities = sqlc.arg('capabilities'),
    resource_scope = sqlc.arg('resource_scope'),
    version = version + 1,
    updated_by = sqlc.arg('actor_user_id'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND version = sqlc.arg('version')
RETURNING *;

-- name: SetWorkspaceAccessGrantStatus :one
UPDATE workspace_access_grant
SET status = sqlc.arg('status'),
    disabled_at = CASE WHEN sqlc.arg('status')::text = 'disabled' THEN now() ELSE NULL END,
    version = version + 1,
    updated_by = sqlc.arg('actor_user_id'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
RETURNING *;

-- name: CreateWorkspaceAccessToken :one
INSERT INTO workspace_access_token (
    grant_id,
    name,
    token_hash,
    token_prefix,
    expires_at,
    created_by
)
VALUES (
    sqlc.arg('grant_id'),
    sqlc.arg('name'),
    sqlc.arg('token_hash'),
    sqlc.arg('token_prefix'),
    sqlc.narg('expires_at'),
    sqlc.arg('actor_user_id')
)
RETURNING *;

-- name: ListWorkspaceAccessTokens :many
SELECT t.*
FROM workspace_access_token t
JOIN workspace_access_grant g ON g.id = t.grant_id
WHERE t.grant_id = sqlc.arg('grant_id')
  AND g.workspace_id = sqlc.arg('workspace_id')
ORDER BY t.created_at DESC;

-- name: UpdateWorkspaceAccessTokenExpiry :one
UPDATE workspace_access_token t
SET expires_at = sqlc.narg('expires_at')
FROM workspace_access_grant g
WHERE t.id = sqlc.arg('id')
  AND t.grant_id = sqlc.arg('grant_id')
  AND g.id = t.grant_id
  AND g.workspace_id = sqlc.arg('workspace_id')
  AND t.revoked_at IS NULL
RETURNING t.*;

-- name: RevokeWorkspaceAccessToken :one
UPDATE workspace_access_token t
SET revoked_at = now(),
    revoked_by = sqlc.arg('actor_user_id')
FROM workspace_access_grant g
WHERE t.id = sqlc.arg('id')
  AND t.grant_id = sqlc.arg('grant_id')
  AND g.id = t.grant_id
  AND g.workspace_id = sqlc.arg('workspace_id')
  AND t.revoked_at IS NULL
RETURNING t.*;

-- name: GetWorkspaceAccessTokenByHash :one
SELECT
    t.id AS token_id,
    t.grant_id,
    t.expires_at,
    t.last_used_at,
    t.revoked_at,
    g.workspace_id,
    g.subject_user_id,
    g.name AS grant_name,
    g.capabilities,
    g.resource_scope,
    g.status AS grant_status,
    g.version AS grant_version,
    u.principal_type
FROM workspace_access_token t
JOIN workspace_access_grant g ON g.id = t.grant_id
JOIN "user" u ON u.id = g.subject_user_id
WHERE t.token_hash = sqlc.arg('token_hash');

-- name: UpdateWorkspaceAccessTokenLastUsed :exec
UPDATE workspace_access_token
SET last_used_at = now()
WHERE id = sqlc.arg('id')
  AND (last_used_at IS NULL OR last_used_at < now() - interval '5 minutes');

-- name: CreateWorkspaceAccessAudit :one
INSERT INTO workspace_access_audit (
    workspace_id,
    grant_id,
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
    sqlc.narg('grant_id'),
    sqlc.narg('token_id'),
    sqlc.narg('actor_user_id'),
    sqlc.arg('action'),
    sqlc.narg('resource_type'),
    sqlc.narg('resource_id'),
    sqlc.arg('result'),
    sqlc.narg('request_id')
)
RETURNING *;
