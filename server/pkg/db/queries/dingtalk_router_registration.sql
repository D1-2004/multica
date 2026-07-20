-- Durable saga state for Multica robot installation <-> Router trusted-source
-- registration. Every state transition is fenced by installation identity and
-- prior status so retries cannot activate a stale agent move or re-install.

-- name: GetDingTalkInstallationForRouterReconciliation :one
SELECT ci.*
FROM channel_installation ci
JOIN agent a
  ON a.id = ci.agent_id
 AND a.workspace_id = ci.workspace_id
JOIN member m
  ON m.workspace_id = ci.workspace_id
 AND m.user_id = ci.installer_user_id
WHERE ci.id = sqlc.arg('id')
  AND ci.channel_type = 'dingtalk';

-- name: ListDingTalkInstallationsForRouterReconciliation :many
SELECT ci.*
FROM channel_installation ci
JOIN agent a
  ON a.id = ci.agent_id
 AND a.workspace_id = ci.workspace_id
JOIN member m
  ON m.workspace_id = ci.workspace_id
 AND m.user_id = ci.installer_user_id
WHERE ci.channel_type = 'dingtalk'
  AND ci.status = 'pending'
  AND (
    ci.config ->> 'router_registration_status' IN (
        'router_pending', 'revoke_pending'
    )
    OR ci.config ->> 'ingress_cutover_state' = 'callback_pending'
  )
ORDER BY ci.updated_at ASC, ci.id ASC;

-- name: UpdateDingTalkInstallationRouterState :one
UPDATE channel_installation
SET config = sqlc.arg('config'),
    status = sqlc.arg('status'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND agent_id = sqlc.arg('agent_id')
  AND channel_type = 'dingtalk'
  AND config ->> 'app_id' = sqlc.arg('client_id')::text
  AND status = sqlc.arg('expected_status')
  AND COALESCE(config ->> 'router_registration_status', '') =
      sqlc.arg('expected_router_registration_status')::text
  AND COALESCE(config ->> 'ingress_cutover_state', '') =
      sqlc.arg('expected_ingress_cutover_state')::text
RETURNING *;
