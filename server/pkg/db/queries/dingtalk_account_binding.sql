-- DingTalk account binding storage. All statements pin channel_type so this
-- integration cannot read or mutate another channel's installation.

-- name: BeginDingTalkAccountBinding :one
-- Insert the first pending attempt only when the agent belongs to the supplied
-- workspace. Concurrent/repeated attempts rotate the callback credential but
-- keep the stable endpoint fields. active rows deliberately return no row.
INSERT INTO channel_installation (
    workspace_id, agent_id, channel_type, config, status, installer_user_id
)
SELECT
    sqlc.arg('workspace_id'),
    sqlc.arg('agent_id'),
    'dingtalk_account',
    sqlc.arg('config'),
    'pending',
    sqlc.arg('installer_user_id')
FROM agent a
WHERE a.id = sqlc.arg('agent_id')
  AND a.workspace_id = sqlc.arg('workspace_id')
ON CONFLICT (workspace_id, agent_id, channel_type) DO UPDATE SET
    config = EXCLUDED.config || jsonb_build_object(
        'dispatch_endpoint_id', channel_installation.config ->> 'dispatch_endpoint_id',
        'dispatch_key_id', channel_installation.config ->> 'dispatch_key_id',
        'dispatch_url', channel_installation.config ->> 'dispatch_url'
    ),
    status = 'pending',
    installer_user_id = EXCLUDED.installer_user_id,
    updated_at = now()
WHERE channel_installation.channel_type = 'dingtalk_account'
  AND channel_installation.status IN ('pending', 'revoked')
RETURNING channel_installation.*;

-- name: GetDingTalkAccountBinding :one
-- The callback route is public and only has an installation id. The agent join
-- makes an orphaned row inert before token verification.
SELECT ci.*
FROM channel_installation ci
JOIN agent a
  ON a.id = ci.agent_id
 AND a.workspace_id = ci.workspace_id
WHERE ci.id = sqlc.arg('id')
  AND ci.channel_type = 'dingtalk_account';

-- name: GetDingTalkAccountBindingByAgent :one
SELECT ci.*
FROM channel_installation ci
JOIN agent a
  ON a.id = ci.agent_id
 AND a.workspace_id = ci.workspace_id
WHERE ci.workspace_id = sqlc.arg('workspace_id')
  AND ci.agent_id = sqlc.arg('agent_id')
  AND ci.channel_type = 'dingtalk_account';

-- name: GetDingTalkAccountBindingInWorkspace :one
SELECT ci.*
FROM channel_installation ci
JOIN agent a
  ON a.id = ci.agent_id
 AND a.workspace_id = ci.workspace_id
WHERE ci.id = sqlc.arg('id')
  AND ci.workspace_id = sqlc.arg('workspace_id')
  AND ci.channel_type = 'dingtalk_account';

-- name: ListDingTalkAccountBindings :many
SELECT ci.*
FROM channel_installation ci
JOIN agent a
  ON a.id = ci.agent_id
 AND a.workspace_id = ci.workspace_id
WHERE ci.workspace_id = sqlc.arg('workspace_id')
  AND ci.channel_type = 'dingtalk_account'
ORDER BY ci.created_at ASC, ci.id ASC;

-- name: ClearExpiredDingTalkAccountCallbackCredentials :exec
-- Callback credentials are only needed for short-lived idempotent retries.
-- Remove both fields together after expiry while preserving the active binding
-- and its stable dispatch endpoint.
UPDATE channel_installation
SET config = config - 'callback_token_hash' - 'callback_expires_at',
    updated_at = now()
WHERE workspace_id = sqlc.arg('workspace_id')
  AND channel_type = 'dingtalk_account'
  AND NULLIF(config ->> 'callback_token_hash', '') IS NOT NULL
  AND NULLIF(config ->> 'callback_expires_at', '')::timestamptz
      <= sqlc.arg('expired_before')::timestamptz;

-- name: ActivateDingTalkAccountBinding :one
-- Callback completion is a compare-and-swap over every ownership dimension and
-- the current callback attempt. A stale QR can never activate a newer pending
-- attempt or a different agent's row.
UPDATE channel_installation
SET config = sqlc.arg('config'),
    status = 'active',
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND agent_id = sqlc.arg('agent_id')
  AND channel_type = 'dingtalk_account'
  AND status = 'pending'
  AND config ->> 'callback_token_hash' = sqlc.arg('expected_callback_token_hash')::text
RETURNING *;

-- name: RevokeDingTalkAccountBinding :one
-- Router DELETE happens before this local transition. The endpoint and other
-- config are retained so a later begin can reuse the stable dispatch URL.
UPDATE channel_installation
SET status = 'revoked',
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND agent_id = sqlc.arg('agent_id')
  AND channel_type = 'dingtalk_account'
  AND status IN ('pending', 'active', 'revoked')
RETURNING *;

-- name: GetActiveDingTalkAccountBindingByEndpoint :one
-- Public dispatch resolution must fail closed when the workspace or agent was
-- deleted, or when the installer is no longer a workspace member. No secret is
-- selected: delivery authentication is derived and verified in the Go layer.
SELECT ci.*
FROM channel_installation ci
JOIN workspace w
  ON w.id = ci.workspace_id
JOIN agent a
  ON a.id = ci.agent_id
 AND a.workspace_id = ci.workspace_id
JOIN member m
  ON m.workspace_id = ci.workspace_id
 AND m.user_id = ci.installer_user_id
WHERE ci.channel_type = 'dingtalk_account'
  AND ci.status = 'active'
  AND ci.config ->> 'dispatch_endpoint_id' = sqlc.arg('dispatch_endpoint_id')::text;
