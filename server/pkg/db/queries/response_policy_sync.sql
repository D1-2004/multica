-- name: ObserveResponsePolicyRollout :one
INSERT INTO dingtalk_response_policy_rollout (target_identity, revision, enabled)
VALUES (@target_identity, @revision, @enabled)
ON CONFLICT (target_identity) DO UPDATE SET
    revision = GREATEST(dingtalk_response_policy_rollout.revision, EXCLUDED.revision),
    enabled = CASE WHEN EXCLUDED.revision > dingtalk_response_policy_rollout.revision
        THEN EXCLUDED.enabled ELSE dingtalk_response_policy_rollout.enabled END,
    updated_at = CASE WHEN EXCLUDED.revision > dingtalk_response_policy_rollout.revision
        THEN now() ELSE dingtalk_response_policy_rollout.updated_at END
RETURNING *;

-- name: HasResponsePolicySyncTarget :one
SELECT EXISTS (SELECT 1 FROM dingtalk_response_policy_sync WHERE target_identity = @target_identity)::boolean;

-- name: ListResponsePolicySyncCandidates :many
SELECT a.id AS agent_id, a.workspace_id, a.runtime_id, a.inbound_coordinator,
    a.dingtalk_show_ai_tag, a.dingtalk_response_policy_revision,
    ci.id AS installation_id,
    (ci.config->>'router_source_id')::text AS source_id,
    GREATEST(a.updated_at, ci.updated_at, r.updated_at)::timestamptz AS source_updated_at
FROM channel_installation ci
JOIN agent a ON a.id = ci.agent_id AND a.workspace_id = ci.workspace_id
LEFT JOIN agent_runtime r ON r.id = a.runtime_id AND r.workspace_id = a.workspace_id
WHERE ci.channel_type = 'dingtalk_account' AND ci.status = 'active' AND a.archived_at IS NULL
  AND COALESCE(ci.config->>'router_source_id', '') <> ''
  AND ci.id > @after_id::uuid
ORDER BY ci.id
LIMIT @batch_size;

-- name: UpsertResponsePolicySync :one
-- Both the source snapshot and rollout fence are rechecked at persistence.
INSERT INTO dingtalk_response_policy_sync (
    target_identity, source_id, installation_id, workspace_id, agent_id,
    agent_revision, rollout_revision, source_updated_at, policy_revision, desired_mode, show_ai_tag
)
SELECT @target_identity, @source_id, ci.id, ci.workspace_id, ci.agent_id,
    @agent_revision, @rollout_revision, @source_updated_at, @agent_revision, @desired_mode, @show_ai_tag
FROM channel_installation ci
JOIN agent a ON a.id = ci.agent_id AND a.workspace_id = ci.workspace_id
LEFT JOIN agent_runtime r ON r.id = a.runtime_id AND r.workspace_id = a.workspace_id
JOIN dingtalk_response_policy_rollout fence ON fence.target_identity = @target_identity
    AND fence.revision = @rollout_revision
WHERE ci.id = @installation_id AND ci.workspace_id = @workspace_id AND ci.agent_id = @agent_id
  AND ci.status = 'active' AND ci.channel_type = 'dingtalk_account' AND a.archived_at IS NULL
  AND ci.config->>'router_source_id' = @source_id
  AND a.dingtalk_response_policy_revision = @agent_revision
  AND GREATEST(a.updated_at, ci.updated_at, r.updated_at) = @source_updated_at
  AND (fence.enabled OR @desired_mode = 'legacy')
ON CONFLICT (target_identity, source_id) DO UPDATE SET
    installation_id = EXCLUDED.installation_id,
    workspace_id = EXCLUDED.workspace_id,
    agent_id = EXCLUDED.agent_id,
    agent_revision = EXCLUDED.agent_revision,
    rollout_revision = EXCLUDED.rollout_revision,
    source_updated_at = EXCLUDED.source_updated_at,
    policy_revision = GREATEST(dingtalk_response_policy_sync.policy_revision + 1, EXCLUDED.agent_revision),
    desired_mode = EXCLUDED.desired_mode,
    show_ai_tag = EXCLUDED.show_ai_tag,
    status = 'pending', attempt_count = 0, available_at = now(),
    lease_token = NULL, lease_expires_at = NULL, last_error = NULL, updated_at = now()
WHERE EXCLUDED.rollout_revision >= dingtalk_response_policy_sync.rollout_revision
  AND EXCLUDED.source_updated_at >= dingtalk_response_policy_sync.source_updated_at
  AND (EXCLUDED.agent_id <> dingtalk_response_policy_sync.agent_id
       OR EXCLUDED.agent_revision >= dingtalk_response_policy_sync.agent_revision)
  AND (EXCLUDED.agent_revision <> dingtalk_response_policy_sync.agent_revision
       OR EXCLUDED.rollout_revision <> dingtalk_response_policy_sync.rollout_revision
       OR EXCLUDED.agent_id <> dingtalk_response_policy_sync.agent_id
       OR EXCLUDED.installation_id <> dingtalk_response_policy_sync.installation_id
       OR EXCLUDED.desired_mode <> dingtalk_response_policy_sync.desired_mode
       OR EXCLUDED.show_ai_tag <> dingtalk_response_policy_sync.show_ai_tag)
RETURNING *;

-- name: ClaimResponsePolicySync :one
WITH candidate AS (
    SELECT sync.id
    FROM dingtalk_response_policy_sync sync
    JOIN dingtalk_response_policy_rollout fence ON fence.target_identity = sync.target_identity
        AND fence.revision = sync.rollout_revision
    JOIN channel_installation ci ON ci.id = sync.installation_id AND ci.workspace_id = sync.workspace_id
        AND ci.agent_id = sync.agent_id AND ci.config->>'router_source_id' = sync.source_id
    JOIN agent a ON a.id = sync.agent_id AND a.workspace_id = sync.workspace_id
    WHERE sync.target_identity = @target_identity AND sync.available_at <= now()
      AND (sync.status <> 'syncing' OR sync.lease_expires_at <= now())
      AND ci.status = 'active' AND ci.channel_type = 'dingtalk_account' AND a.archived_at IS NULL
      AND a.dingtalk_response_policy_revision = sync.agent_revision
    ORDER BY sync.available_at, sync.id
    FOR UPDATE OF sync SKIP LOCKED
    LIMIT 1
)
UPDATE dingtalk_response_policy_sync sync
SET status = 'syncing', lease_token = gen_random_uuid(), lease_expires_at = now() + interval '30 seconds',
    attempt_count = sync.attempt_count + 1, updated_at = now()
FROM candidate
WHERE sync.id = candidate.id
RETURNING sync.*;

-- name: CompleteResponsePolicySync :execrows
UPDATE dingtalk_response_policy_sync
SET status = 'synced', lease_token = NULL, lease_expires_at = NULL,
    observed_policy = @observed_policy, synced_at = now(), last_error = NULL,
    attempt_count = 0, available_at = now() + interval '5 minutes', updated_at = now()
WHERE id = @id AND lease_token = @lease_token AND status = 'syncing';

-- name: RetryResponsePolicySync :execrows
UPDATE dingtalk_response_policy_sync
SET status = 'pending', lease_token = NULL, lease_expires_at = NULL,
    last_error = @last_error, available_at = @available_at, updated_at = now()
WHERE id = @id AND lease_token = @lease_token AND status = 'syncing';
