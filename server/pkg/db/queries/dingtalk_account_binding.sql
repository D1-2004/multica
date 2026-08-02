-- DingTalk account binding storage. All statements pin channel_type so this
-- integration cannot read or mutate another channel's installation.

-- name: BeginDingTalkAccountBinding :one
-- Insert the first pending attempt only when the agent belongs to the supplied
-- workspace. Concurrent/repeated attempts rotate the callback credential but
-- keep the stable endpoint fields. The KEY SHARE lock conflicts with Agent
-- teardown's FOR UPDATE lock; after teardown commits, archived agents no longer
-- satisfy the CTE and cannot acquire a new pending binding. active rows
-- deliberately return no row.
WITH target_agent AS (
    SELECT id, workspace_id
    FROM agent
    WHERE id = sqlc.arg('agent_id')
      AND workspace_id = sqlc.arg('workspace_id')
      AND archived_at IS NULL
    FOR KEY SHARE
)
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
FROM target_agent a
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

-- name: GetDingTalkAccountBindingByAgentForUpdate :one
-- Agent teardown already holds the Agent row lock. Lock the single local
-- projection as the second step so callback activation and local cleanup are
-- serialized behind the same transaction.
SELECT ci.*
FROM channel_installation ci
WHERE ci.agent_id = sqlc.arg('agent_id')
  AND ci.channel_type = 'dingtalk_account'
FOR UPDATE;

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

-- name: ListDingTalkAccountBindingAccountKeyBackfillRows :many
-- The explicit maintenance command must inventory orphaned projections after
-- Agent deletion, so this query deliberately has no Agent join.
SELECT ci.*
FROM channel_installation ci
WHERE ci.channel_type = 'dingtalk_account'
ORDER BY ci.created_at ASC, ci.id ASC;

-- name: BackfillDingTalkAccountRouterAccountKey :one
-- Enrichment writes only an all-or-nothing account identity and compares the
-- complete original JSON snapshot so a concurrent callback always wins.
UPDATE channel_installation
SET config = config || jsonb_build_object(
        'router_platform', sqlc.arg('router_platform')::text,
        'router_tenant_id', sqlc.arg('router_tenant_id')::text,
        'router_account_id', sqlc.arg('router_account_id')::text
    ),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND agent_id = sqlc.arg('agent_id')
  AND channel_type = 'dingtalk_account'
  AND status = sqlc.arg('expected_status')::text
  AND config ->> 'router_source_id' = sqlc.arg('expected_router_source_id')::text
  AND NULLIF(config ->> 'router_platform', '') IS NULL
  AND NULLIF(config ->> 'router_tenant_id', '') IS NULL
  AND NULLIF(config ->> 'router_account_id', '') IS NULL
  AND config = sqlc.arg('expected_config')::jsonb
RETURNING *;

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

-- name: UpdateDingTalkAccountBindingSurface :one
-- Persist the Router's authoritative processing surface without rebuilding or
-- changing any other message-route snapshots. The source id is a CAS guard so
-- an update for an older binding cannot overwrite a replacement binding.
UPDATE channel_installation
SET config = jsonb_set(
        config,
        '{surface_type}',
        to_jsonb(sqlc.arg('surface_type')::text),
        true
    ),
    updated_at = now()
WHERE workspace_id = sqlc.arg('workspace_id')
  AND agent_id = sqlc.arg('agent_id')
  AND channel_type = 'dingtalk_account'
  AND status = 'active'
  AND config ->> 'router_source_id' = sqlc.arg('expected_router_source_id')::text
RETURNING *;

-- name: CompleteDingTalkAccountBindingResult :one
-- Record a terminal result that did not create a Router source. The caller
-- keeps failures pending so a later begin can issue a fresh attempt.
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

-- name: RevokeDingTalkAccountBinding :one
-- Router DELETE happens before this local transition. Retain only the stable
-- dispatch endpoint fields needed by a later begin; remove all callback,
-- source, account, avatar, scope, conversation, and binding-time snapshots.
-- Message routes and execution identities have independent lifecycles, so this
-- statement never mutates agent_dingtalk_identity or its in-flight attempts.
UPDATE channel_installation
SET config = jsonb_build_object(
        'schema_version', config -> 'schema_version',
        'dispatch_endpoint_id', config -> 'dispatch_endpoint_id',
        'dispatch_key_id', config -> 'dispatch_key_id',
        'dispatch_url', config -> 'dispatch_url'
    ),
    status = 'revoked',
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND agent_id = sqlc.arg('agent_id')
  AND channel_type = 'dingtalk_account'
  AND status IN ('pending', 'active', 'revoked')
RETURNING *;

-- name: RevokeDingTalkAccountBindingByAccountKey :one
-- A conditional Router unbind may only revoke the exact local account key that
-- was sent upstream. A concurrent takeover or callback cannot be overwritten.
UPDATE channel_installation
SET config = jsonb_build_object(
        'schema_version', config -> 'schema_version',
        'dispatch_endpoint_id', config -> 'dispatch_endpoint_id',
        'dispatch_key_id', config -> 'dispatch_key_id',
        'dispatch_url', config -> 'dispatch_url'
    ),
    status = 'revoked',
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND agent_id = sqlc.arg('agent_id')
  AND channel_type = 'dingtalk_account'
  AND status = 'active'
  AND config ->> 'router_platform' = sqlc.arg('expected_router_platform')::text
  AND config ->> 'router_tenant_id' = sqlc.arg('expected_router_tenant_id')::text
  AND config ->> 'router_account_id' = sqlc.arg('expected_router_account_id')::text
RETURNING *;

-- name: DeletePreviousDingTalkAccountBindingProjection :many
-- A takeover callback removes only the previous Agent projection for this
-- canonical account in the current database. The winning installation is
-- always excluded and this statement has no Router side effects.
DELETE FROM channel_installation
WHERE channel_type = 'dingtalk_account'
  AND id <> sqlc.arg('current_installation_id')
  AND agent_id = sqlc.arg('previous_agent_id')
  AND config ->> 'router_platform' = sqlc.arg('router_platform')::text
  AND config ->> 'router_tenant_id' = sqlc.arg('router_tenant_id')::text
  AND config ->> 'router_account_id' = sqlc.arg('router_account_id')::text
RETURNING *;

-- name: DeleteDingTalkAccountBindingProjectionForTeardown :one
-- Router has already conditionally unbound this exact BindingKey. Remove only
-- the still-active local projection whose complete AccountKey matches the
-- request; a concurrent takeover/callback therefore cannot be destroyed.
DELETE FROM channel_installation
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND agent_id = sqlc.arg('agent_id')
  AND channel_type = 'dingtalk_account'
  AND status = 'active'
  AND config ->> 'router_platform' = sqlc.arg('expected_router_platform')::text
  AND config ->> 'router_tenant_id' = sqlc.arg('expected_router_tenant_id')::text
  AND config ->> 'router_account_id' = sqlc.arg('expected_router_account_id')::text
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
