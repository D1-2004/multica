-- name: GetActiveCloudSandboxSession :one
SELECT *
FROM fc_e2b_sandbox_session
WHERE runtime_id = sqlc.arg('runtime_id')
  AND scope_type = sqlc.arg('scope_type')
  AND scope_id = sqlc.arg('scope_id')
  AND sandbox_backend = sqlc.arg('sandbox_backend')
  AND identity_fingerprint = sqlc.arg('identity_fingerprint')
  AND artifact_ref = sqlc.arg('artifact_ref')
  AND status = 'running'
  AND expires_at > now()
ORDER BY updated_at DESC
LIMIT 1;

-- name: UpsertCloudSandboxSession :one
INSERT INTO fc_e2b_sandbox_session (
    workspace_id,
    runtime_id,
    scope_type,
    scope_id,
    sandbox_id,
    template,
    status,
    last_used_at,
    expires_at,
    sandbox_backend,
    identity_fingerprint,
    artifact_ref
)
VALUES (
    sqlc.arg('workspace_id'),
    sqlc.arg('runtime_id'),
    sqlc.arg('scope_type'),
    sqlc.arg('scope_id'),
    sqlc.arg('sandbox_id'),
    sqlc.arg('artifact_ref'),
    'running',
    now(),
    sqlc.arg('expires_at'),
    sqlc.arg('sandbox_backend'),
    sqlc.arg('identity_fingerprint'),
    sqlc.arg('artifact_ref')
)
ON CONFLICT (
    runtime_id,
    scope_type,
    scope_id,
    sandbox_backend,
    identity_fingerprint
)
DO UPDATE SET
    sandbox_id = EXCLUDED.sandbox_id,
    template = EXCLUDED.template,
    status = 'running',
    last_used_at = now(),
    expires_at = EXCLUDED.expires_at,
    artifact_ref = EXCLUDED.artifact_ref,
    updated_at = now()
RETURNING *;

-- name: TouchCloudSandboxSession :exec
UPDATE fc_e2b_sandbox_session
SET last_used_at = now(),
    updated_at = now()
WHERE runtime_id = sqlc.arg('runtime_id')
  AND scope_type = sqlc.arg('scope_type')
  AND scope_id = sqlc.arg('scope_id')
  AND sandbox_id = sqlc.arg('sandbox_id')
  AND sandbox_backend = sqlc.arg('sandbox_backend')
  AND identity_fingerprint = sqlc.arg('identity_fingerprint')
  AND status = 'running';

-- name: MarkCloudSandboxSessionStale :exec
UPDATE fc_e2b_sandbox_session
SET status = 'stale',
    updated_at = now()
WHERE runtime_id = sqlc.arg('runtime_id')
  AND scope_type = sqlc.arg('scope_type')
  AND scope_id = sqlc.arg('scope_id')
  AND sandbox_id = sqlc.arg('sandbox_id')
  AND sandbox_backend = sqlc.arg('sandbox_backend')
  AND identity_fingerprint = sqlc.arg('identity_fingerprint');

-- name: MarkCloudSandboxSessionsStaleByIdentity :execrows
UPDATE fc_e2b_sandbox_session
SET status = 'stale',
    updated_at = now()
WHERE workspace_id = sqlc.arg('workspace_id')
  AND runtime_id = sqlc.arg('runtime_id')
  AND sandbox_backend = sqlc.arg('sandbox_backend')
  AND identity_fingerprint = sqlc.arg('identity_fingerprint')
  AND status = 'running';

-- name: MarkCloudSandboxSessionsStaleByRuntimeAndBackend :execrows
UPDATE fc_e2b_sandbox_session
SET status = 'stale',
    updated_at = now()
WHERE runtime_id = sqlc.arg('runtime_id')
  AND sandbox_backend = sqlc.arg('sandbox_backend')
  AND status = 'running';

-- name: ListActiveCloudSandboxSessionsByAgentIdentity :many
SELECT session.*
FROM fc_e2b_sandbox_session session
JOIN agent_runtime runtime ON runtime.id = session.runtime_id
JOIN agent ON agent.runtime_id = runtime.id
WHERE session.workspace_id = sqlc.arg('workspace_id')
  AND agent.id = sqlc.arg('agent_id')
  AND session.sandbox_backend = 'asb'
  AND session.identity_fingerprint = sqlc.arg('identity_fingerprint')
  AND session.status = 'running';
