-- name: CreateDingTalkInstallSession :exec
-- Opens a device-flow install session. Written by the pod that served
-- /install/begin; readable from every pod, which is the whole point (see
-- migration 181).
INSERT INTO dingtalk_install_session (
    id, workspace_id, agent_id, expires_at
) VALUES ($1, $2, $3, $4);

-- name: GetDingTalkInstallSession :one
SELECT * FROM dingtalk_install_session WHERE id = $1;

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
