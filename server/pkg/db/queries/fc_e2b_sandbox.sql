-- name: GetActiveFCE2BSandboxSession :one
SELECT * FROM fc_e2b_sandbox_session
WHERE runtime_id = $1
  AND scope_type = $2
  AND scope_id = $3
  AND status = 'running'
  AND expires_at > now()
ORDER BY updated_at DESC
LIMIT 1;

-- name: UpsertFCE2BSandboxSession :one
INSERT INTO fc_e2b_sandbox_session (
    workspace_id, runtime_id, scope_type, scope_id,
    sandbox_id, template, status, last_used_at, expires_at
)
VALUES ($1, $2, $3, $4, $5, $6, 'running', now(), $7)
ON CONFLICT (runtime_id, scope_type, scope_id)
DO UPDATE SET
    sandbox_id = EXCLUDED.sandbox_id,
    template = EXCLUDED.template,
    status = 'running',
    last_used_at = now(),
    expires_at = EXCLUDED.expires_at,
    updated_at = now()
RETURNING *;

-- name: TouchFCE2BSandboxSession :exec
UPDATE fc_e2b_sandbox_session
SET last_used_at = now(), updated_at = now()
WHERE runtime_id = $1
  AND scope_type = $2
  AND scope_id = $3
  AND sandbox_id = $4
  AND status = 'running';

-- name: MarkFCE2BSandboxSessionStale :exec
UPDATE fc_e2b_sandbox_session
SET status = 'stale', updated_at = now()
WHERE runtime_id = $1
  AND scope_type = $2
  AND scope_id = $3
  AND sandbox_id = $4;
