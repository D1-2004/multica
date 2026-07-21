-- name: CreateDingTalkInstallSession :one
-- Opens a device-flow install session. Written by the pod that served
-- /install/begin; readable from every pod, which is the whole point (see
-- migration 181).
WITH generation_lock AS (
    SELECT pg_advisory_xact_lock(
        hashtextextended(
            sqlc.arg('workspace_id')::uuid::text || ':' ||
            sqlc.arg('agent_id')::uuid::text,
            0
        )
    )
), next_generation AS (
    SELECT COALESCE(MAX(s.generation), 0) + 1 AS generation
    FROM dingtalk_install_session s, generation_lock
    WHERE s.workspace_id = sqlc.arg('workspace_id')::uuid
      AND s.agent_id = sqlc.arg('agent_id')::uuid
)
INSERT INTO dingtalk_install_session (
    id, workspace_id, agent_id, expires_at,
    transport_mode, allow_unbound, generation
)
SELECT sqlc.arg('id'), sqlc.arg('workspace_id')::uuid, sqlc.arg('agent_id')::uuid, sqlc.arg('expires_at'),
       sqlc.arg('transport_mode'), sqlc.arg('allow_unbound'), next_generation.generation
FROM next_generation
RETURNING generation;

-- name: GetDingTalkInstallSession :one
SELECT * FROM dingtalk_install_session WHERE id = $1;

-- name: IsCurrentDingTalkInstallSession :one
SELECT NOT EXISTS (
    SELECT 1
    FROM dingtalk_install_session newer
    WHERE newer.workspace_id = sqlc.arg('workspace_id')
      AND newer.agent_id = sqlc.arg('agent_id')
      AND newer.generation > sqlc.arg('generation')
) AS is_current;

-- name: FinishDingTalkInstallSessionSuccess :exec
-- Terminal transition; only a still-pending row moves, so a late poll result
-- cannot overwrite an already-recorded outcome.
UPDATE dingtalk_install_session
SET status = 'success',
    installation_id = $2,
    gc_after = $3,
    updated_at = now()
WHERE id = $1 AND status = 'pending';

-- name: FinishDingTalkInstallSessionError :exec
UPDATE dingtalk_install_session
SET status = 'error',
    error_reason = $2,
    error_message = $3,
    gc_after = $4,
    updated_at = now()
WHERE id = $1 AND status = 'pending';

-- name: SweepDingTalkInstallSessions :exec
-- Drops terminal rows past their gc window, plus pending rows whose driving
-- pod disappeared (still pending well after the device code expired).
DELETE FROM dingtalk_install_session
WHERE (gc_after IS NOT NULL AND gc_after < $1)
   OR (status = 'pending' AND expires_at < $1 - INTERVAL '1 hour');
