-- name: CountDingTalkBindingUnbindReadinessByAgentIDs :one
-- Agent removal must not silently skip an active historical projection or
-- proceed without a Router target. The handler compares these counts inside
-- the removal transaction before it persists any intent.
SELECT
    count(*)::bigint AS active_count,
    count(*) FILTER (WHERE
        ci.config ->> 'router_platform' = 'dingtalk'
        AND ci.config ->> 'router_tenant_id' = btrim(ci.config ->> 'router_tenant_id')
        AND ci.config ->> 'router_tenant_id' <> ''
        AND ci.config ->> 'router_account_id' = btrim(ci.config ->> 'router_account_id')
        AND ci.config ->> 'router_account_id' <> ''
    )::bigint AS ready_count
FROM channel_installation ci
WHERE ci.channel_type = 'dingtalk_account'
  AND ci.agent_id = ANY(sqlc.arg('agent_ids')::uuid[])
  AND ci.status = 'active';

-- name: EnqueueDingTalkBindingUnbindsByAgentIDs :exec
INSERT INTO dingtalk_binding_unbind_outbox (
    installation_id,
    workspace_id,
    agent_id,
    platform,
    tenant_id,
    account_id,
    target_identity
)
SELECT
    ci.id,
    ci.workspace_id,
    ci.agent_id,
    ci.config ->> 'router_platform',
    ci.config ->> 'router_tenant_id',
    ci.config ->> 'router_account_id',
    sqlc.arg('target_identity')
FROM channel_installation ci
WHERE ci.channel_type = 'dingtalk_account'
  AND ci.agent_id = ANY(sqlc.arg('agent_ids')::uuid[])
  AND ci.status = 'active'
  AND ci.config ->> 'router_platform' = 'dingtalk'
  AND NULLIF(btrim(ci.config ->> 'router_tenant_id'), '') IS NOT NULL
  AND NULLIF(btrim(ci.config ->> 'router_account_id'), '') IS NOT NULL
ON CONFLICT DO NOTHING;

-- name: ClaimDingTalkBindingUnbind :one
WITH candidate AS (
    SELECT id
    FROM dingtalk_binding_unbind_outbox queued
    WHERE queued.status = 'queued'
      AND queued.target_identity = sqlc.arg('worker_target_identity')
      AND queued.available_at <= now()
      AND (queued.lease_expires_at IS NULL OR queued.lease_expires_at <= now())
    ORDER BY queued.available_at, queued.created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE dingtalk_binding_unbind_outbox AS intent
SET lease_token = gen_random_uuid(),
    lease_expires_at = now() + interval '2 minutes',
    updated_at = now()
FROM candidate
WHERE intent.id = candidate.id
RETURNING intent.*;

-- name: CompleteDingTalkBindingUnbind :one
WITH delivered AS (
    UPDATE dingtalk_binding_unbind_outbox AS intent
    SET status = 'delivered',
        attempt_count = intent.attempt_count + 1,
        lease_token = NULL,
        lease_expires_at = NULL,
        last_error_code = NULL,
        delivered_at = now(),
        updated_at = now()
    WHERE intent.id = sqlc.arg('id')
      AND intent.lease_token = sqlc.arg('lease_token')
      AND intent.status = 'queued'
    RETURNING intent.*
), removed_projection AS (
    DELETE FROM channel_installation ci
    USING delivered
    WHERE ci.id = delivered.installation_id
      AND ci.workspace_id = delivered.workspace_id
      AND ci.agent_id = delivered.agent_id
      AND ci.channel_type = 'dingtalk_account'
      AND ci.config ->> 'router_platform' = delivered.platform
      AND ci.config ->> 'router_tenant_id' = delivered.tenant_id
      AND ci.config ->> 'router_account_id' = delivered.account_id
)
SELECT * FROM delivered;

-- name: RetryDingTalkBindingUnbind :one
UPDATE dingtalk_binding_unbind_outbox AS intent
SET available_at = sqlc.arg('available_at'),
    attempt_count = intent.attempt_count + 1,
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error_code = sqlc.arg('last_error_code'),
    updated_at = now()
WHERE intent.id = sqlc.arg('id')
  AND intent.lease_token = sqlc.arg('lease_token')
  AND intent.status = 'queued'
RETURNING intent.*;
