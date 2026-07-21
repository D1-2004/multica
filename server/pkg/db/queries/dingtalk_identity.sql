-- name: BeginAgentDingTalkIdentityAttempt :one
INSERT INTO agent_dingtalk_identity_attempt (
    workspace_id,
    agent_id,
    initiator_user_id,
    callback_token_hash,
    expires_at
)
SELECT
    sqlc.arg('workspace_id'),
    sqlc.arg('agent_id'),
    sqlc.arg('initiator_user_id'),
    sqlc.arg('callback_token_hash'),
    sqlc.arg('expires_at')
FROM agent a
WHERE a.id = sqlc.arg('agent_id')
  AND a.workspace_id = sqlc.arg('workspace_id')
RETURNING *;

-- name: GetAgentDingTalkIdentityAttempt :one
SELECT attempt.*
FROM agent_dingtalk_identity_attempt attempt
JOIN agent a
  ON a.id = attempt.agent_id
 AND a.workspace_id = attempt.workspace_id
WHERE attempt.id = sqlc.arg('id');

-- name: GetAgentDingTalkIdentityAttemptByAgent :one
SELECT attempt.*
FROM agent_dingtalk_identity_attempt attempt
JOIN agent a
  ON a.id = attempt.agent_id
 AND a.workspace_id = attempt.workspace_id
WHERE attempt.workspace_id = sqlc.arg('workspace_id')
  AND attempt.agent_id = sqlc.arg('agent_id')
ORDER BY attempt.created_at DESC
LIMIT 1;

-- name: CompleteAgentDingTalkIdentityAttempt :one
WITH completed AS (
    UPDATE agent_dingtalk_identity_attempt
    SET completed_uid = sqlc.arg('dws_uid'),
        completed_org_id = sqlc.arg('org_id'),
        used_at = now(),
        updated_at = now()
    WHERE id = sqlc.arg('attempt_id')
      AND callback_token_hash = sqlc.arg('callback_token_hash')
      AND used_at IS NULL
      AND now() < expires_at
    RETURNING *
), upserted AS (
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
        completed.agent_id,
        completed.workspace_id,
        sqlc.arg('dws_uid'),
        sqlc.arg('org_id'),
        sqlc.arg('organization_name'),
        sqlc.arg('account_display_name'),
        sqlc.arg('account_avatar_url'),
        completed.initiator_user_id,
        now(),
        now()
    FROM completed
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
    RETURNING agent_dingtalk_identity.*
)
SELECT * FROM upserted;

-- name: GetAgentDingTalkIdentity :one
SELECT identity.*
FROM agent_dingtalk_identity identity
JOIN agent a
  ON a.id = identity.agent_id
 AND a.workspace_id = identity.workspace_id
WHERE identity.workspace_id = sqlc.arg('workspace_id')
  AND identity.agent_id = sqlc.arg('agent_id');

-- name: ListAgentDingTalkIdentities :many
SELECT identity.*
FROM agent_dingtalk_identity identity
JOIN agent a
  ON a.id = identity.agent_id
 AND a.workspace_id = identity.workspace_id
WHERE identity.workspace_id = sqlc.arg('workspace_id')
ORDER BY identity.bound_at ASC, identity.agent_id ASC;

-- name: DeleteAgentDingTalkIdentity :one
WITH deleted_attempts AS (
    DELETE FROM agent_dingtalk_identity_attempt
    WHERE workspace_id = sqlc.arg('workspace_id')
      AND agent_id = sqlc.arg('agent_id')
)
DELETE FROM agent_dingtalk_identity AS identity
WHERE identity.workspace_id = sqlc.arg('workspace_id')
  AND identity.agent_id = sqlc.arg('agent_id')
RETURNING identity.*;

-- name: DeleteAgentDingTalkIdentityAttempts :exec
DELETE FROM agent_dingtalk_identity_attempt
WHERE workspace_id = sqlc.arg('workspace_id')
  AND agent_id = sqlc.arg('agent_id');
