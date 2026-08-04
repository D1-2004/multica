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

-- name: ListActiveCloudSandboxSessionsByRuntimeAndBackend :many
SELECT *
FROM fc_e2b_sandbox_session
WHERE runtime_id = sqlc.arg('runtime_id')
  AND sandbox_backend = sqlc.arg('sandbox_backend')
  AND status IN ('running', 'stale')
ORDER BY updated_at, sandbox_id;

-- name: ListIdleASBSandboxSessionsByRuntimes :many
SELECT session.*
FROM fc_e2b_sandbox_session AS session
WHERE session.runtime_id = ANY(sqlc.arg('runtime_ids')::uuid[])
  AND session.sandbox_backend = 'asb'
  -- A Runtime artifact or API-key rotation marks the database row stale
  -- before the old ASB instance actually exits. Keep that resource visible
  -- to the quota reclaimer until the control-plane sandbox is deleted.
  AND session.status IN ('running', 'stale')
  AND NOT EXISTS (
      SELECT 1
      FROM agent_task_queue AS task
      WHERE task.runtime_id = session.runtime_id
        AND task.id IS DISTINCT FROM sqlc.narg('excluded_task_id')::uuid
        AND task.status IN (
            'queued',
            'dispatched',
            'running',
            'waiting_local_directory',
            'deferred'
        )
        AND (
            (session.scope_type = 'chat' AND task.chat_session_id = session.scope_id)
            OR
            (session.scope_type = 'issue' AND task.issue_id = session.scope_id)
        )
  )
ORDER BY
    CASE WHEN session.expires_at <= now() THEN 0 ELSE 1 END,
    session.last_used_at,
    session.created_at,
    session.sandbox_id;

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
