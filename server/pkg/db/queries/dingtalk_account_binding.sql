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

-- name: CompleteDingTalkAccountBindingResult :one
-- Record a terminal result that did not create a Router source. The caller
-- chooses active only for an identity-only skipped route; failures stay
-- pending so a later begin can issue a fresh attempt.
UPDATE channel_installation
SET config = sqlc.arg('config'),
    status = sqlc.arg('status'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND agent_id = sqlc.arg('agent_id')
  AND channel_type = 'dingtalk_account'
  AND status = 'pending'
  AND config ->> 'callback_token_hash' = sqlc.arg('expected_callback_token_hash')::text
RETURNING *;

-- name: ActivateDingTalkAccountBindingWithIdentity :one
-- A message binding is not complete until both the Router subscription and
-- the Agent execution identity have been verified. Persist both in one
-- statement so an Agent can never expose an active message route without its
-- corresponding default execution identity.
WITH activated AS (
    UPDATE channel_installation AS installation
    SET config = sqlc.arg('config'),
        status = 'active',
        updated_at = now()
    WHERE installation.id = sqlc.arg('id')
      AND installation.workspace_id = sqlc.arg('workspace_id')
      AND installation.agent_id = sqlc.arg('agent_id')
      AND installation.channel_type = 'dingtalk_account'
      AND installation.status = 'pending'
      AND installation.config ->> 'callback_token_hash' = sqlc.arg('expected_callback_token_hash')::text
    RETURNING installation.*
), upserted_identity AS (
    INSERT INTO agent_dingtalk_identity (
        agent_id,
        workspace_id,
        dws_uid,
        org_id,
        organization_name,
        account_display_name,
        account_avatar_url,
        bound_by,
        bound_at,
        updated_at
    )
    SELECT
        activated.agent_id,
        activated.workspace_id,
        sqlc.arg('dws_uid'),
        sqlc.arg('org_id'),
        sqlc.arg('organization_name'),
        sqlc.arg('account_display_name'),
        sqlc.arg('account_avatar_url'),
        activated.installer_user_id,
        now(),
        now()
    FROM activated
    ON CONFLICT (agent_id) DO UPDATE SET
        workspace_id = EXCLUDED.workspace_id,
        dws_uid = EXCLUDED.dws_uid,
        org_id = EXCLUDED.org_id,
        organization_name = EXCLUDED.organization_name,
        account_display_name = EXCLUDED.account_display_name,
        account_avatar_url = EXCLUDED.account_avatar_url,
        bound_by = EXCLUDED.bound_by,
        bound_at = EXCLUDED.bound_at,
        updated_at = EXCLUDED.updated_at
    WHERE agent_dingtalk_identity.workspace_id = EXCLUDED.workspace_id
    RETURNING agent_id
)
SELECT activated.*
FROM activated
JOIN upserted_identity ON upserted_identity.agent_id = activated.agent_id;

-- name: RevokeDingTalkAccountBinding :one
-- Router DELETE happens before this local transition. Retain only the stable
-- dispatch endpoint fields needed by a later begin; remove all callback,
-- source, account, avatar, scope, conversation, and binding-time snapshots.
-- A successfully activated message binding owns the Agent's default identity,
-- so revoking it removes that identity. Revoking an abandoned pending route
-- preserves any independently completed identity-only binding.
WITH target AS (
    SELECT installation.id, installation.workspace_id, installation.agent_id, installation.status
    FROM channel_installation installation
    WHERE installation.id = sqlc.arg('id')
      AND installation.workspace_id = sqlc.arg('workspace_id')
      AND installation.agent_id = sqlc.arg('agent_id')
      AND installation.channel_type = 'dingtalk_account'
      AND installation.status IN ('pending', 'active', 'revoked')
), deleted_identity AS (
    DELETE FROM agent_dingtalk_identity identity
    USING target
    WHERE identity.workspace_id = target.workspace_id
      AND identity.agent_id = target.agent_id
      AND target.status = 'active'
), deleted_attempts AS (
    DELETE FROM agent_dingtalk_identity_attempt attempt
    USING target
    WHERE attempt.workspace_id = target.workspace_id
      AND attempt.agent_id = target.agent_id
)
UPDATE channel_installation installation
SET config = jsonb_build_object(
        'schema_version', installation.config -> 'schema_version',
        'dispatch_endpoint_id', installation.config -> 'dispatch_endpoint_id',
        'dispatch_key_id', installation.config -> 'dispatch_key_id',
        'dispatch_url', installation.config -> 'dispatch_url'
    ),
    status = 'revoked',
    updated_at = now()
FROM target
WHERE installation.id = target.id
RETURNING installation.*;

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
