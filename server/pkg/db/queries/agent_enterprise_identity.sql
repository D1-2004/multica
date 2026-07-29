-- name: GetAgentEnterpriseIdentity :one
SELECT *
FROM agent_enterprise_identity
WHERE workspace_id = sqlc.arg('workspace_id')
  AND agent_id = sqlc.arg('agent_id');

-- name: GetActiveAgentEnterpriseIdentity :one
SELECT *
FROM agent_enterprise_identity
WHERE workspace_id = sqlc.arg('workspace_id')
  AND agent_id = sqlc.arg('agent_id')
  AND status = 'active';

-- name: UpsertAgentEnterpriseIdentity :one
INSERT INTO agent_enterprise_identity (
    workspace_id,
    agent_id,
    raw_emp_id,
    display_name,
    buc_agent_id,
    agent_spiffe_id,
    aip_id,
    buc_anchor_sandbox_id,
    authx_refresh_token_encrypted,
    authx_refresh_expires_at,
    token_version,
    status,
    bound_by
)
SELECT
    sqlc.arg('workspace_id'),
    sqlc.arg('agent_id'),
    sqlc.arg('raw_emp_id'),
    sqlc.arg('display_name'),
    sqlc.arg('buc_agent_id'),
    sqlc.arg('agent_spiffe_id'),
    sqlc.arg('aip_id'),
    sqlc.arg('buc_anchor_sandbox_id'),
    sqlc.arg('authx_refresh_token_encrypted'),
    sqlc.arg('authx_refresh_expires_at'),
    1,
    'active',
    sqlc.arg('bound_by')
FROM agent
WHERE agent.id = sqlc.arg('agent_id')
  AND agent.workspace_id = sqlc.arg('workspace_id')
ON CONFLICT (workspace_id, agent_id)
DO UPDATE SET
    raw_emp_id = EXCLUDED.raw_emp_id,
    display_name = EXCLUDED.display_name,
    buc_agent_id = EXCLUDED.buc_agent_id,
    agent_spiffe_id = EXCLUDED.agent_spiffe_id,
    aip_id = EXCLUDED.aip_id,
    buc_anchor_sandbox_id = EXCLUDED.buc_anchor_sandbox_id,
    authx_refresh_token_encrypted = EXCLUDED.authx_refresh_token_encrypted,
    authx_refresh_expires_at = EXCLUDED.authx_refresh_expires_at,
    token_version = agent_enterprise_identity.token_version + 1,
    status = 'active',
    bound_by = EXCLUDED.bound_by,
    updated_at = now()
RETURNING agent_enterprise_identity.*;

-- name: RevokeAgentEnterpriseIdentity :one
UPDATE agent_enterprise_identity
SET status = 'revoked',
    buc_anchor_sandbox_id = NULL,
    authx_refresh_token_encrypted = NULL,
    authx_refresh_expires_at = NULL,
    token_version = token_version + 1,
    updated_at = now()
WHERE workspace_id = sqlc.arg('workspace_id')
  AND agent_id = sqlc.arg('agent_id')
  AND status <> 'revoked'
RETURNING *;

-- name: MarkAgentEnterpriseIdentityNeedsReauth :execrows
UPDATE agent_enterprise_identity
SET status = 'needs_reauth',
    buc_anchor_sandbox_id = NULL,
    authx_refresh_token_encrypted = NULL,
    authx_refresh_expires_at = NULL,
    token_version = token_version + 1,
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND token_version = sqlc.arg('expected_token_version')
  AND status = 'active';

-- name: ListAgentEnterpriseIdentitiesForMaintenance :many
SELECT *
FROM agent_enterprise_identity
WHERE status = 'active'
  AND (
    authx_refresh_expires_at <= sqlc.arg('rotate_before')
    OR updated_at <= sqlc.arg('anchor_renew_before')
  )
ORDER BY LEAST(authx_refresh_expires_at, updated_at), id
LIMIT sqlc.arg('batch_size');

-- name: TouchAgentEnterpriseIdentityMaintenance :execrows
UPDATE agent_enterprise_identity
SET updated_at = now()
WHERE id = sqlc.arg('id')
  AND token_version = sqlc.arg('expected_token_version')
  AND status = 'active';

-- name: CompareAndSwapAgentEnterpriseIdentityToken :one
UPDATE agent_enterprise_identity
SET authx_refresh_token_encrypted = sqlc.arg('authx_refresh_token_encrypted'),
    authx_refresh_expires_at = sqlc.arg('authx_refresh_expires_at'),
    token_version = token_version + 1,
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND token_version = sqlc.arg('expected_token_version')
  AND status = 'active'
RETURNING *;

-- name: CreateAgentEnterpriseIdentityAttempt :one
INSERT INTO agent_enterprise_identity_attempt (
    workspace_id,
    agent_id,
    actor_user_id,
    requested_raw_emp_id,
    state_hash,
    nonce_hash,
    pkce_verifier_encrypted,
    redirect_path,
    expires_at
)
SELECT
    sqlc.arg('workspace_id'),
    sqlc.arg('agent_id'),
    sqlc.arg('actor_user_id'),
    sqlc.arg('requested_raw_emp_id'),
    sqlc.arg('state_hash'),
    sqlc.arg('nonce_hash'),
    sqlc.narg('pkce_verifier_encrypted'),
    sqlc.arg('redirect_path'),
    sqlc.arg('expires_at')
FROM agent
JOIN member
    ON member.workspace_id = agent.workspace_id
   AND member.user_id = sqlc.arg('actor_user_id')
WHERE agent.id = sqlc.arg('agent_id')
  AND agent.workspace_id = sqlc.arg('workspace_id')
RETURNING agent_enterprise_identity_attempt.*;

-- name: ConsumeAgentEnterpriseIdentityAttempt :one
UPDATE agent_enterprise_identity_attempt AS attempt
SET consumed_at = now()
FROM agent, member
WHERE attempt.state_hash = sqlc.arg('state_hash')
  AND attempt.consumed_at IS NULL
  AND attempt.expires_at > now()
  AND agent.id = attempt.agent_id
  AND agent.workspace_id = attempt.workspace_id
  AND agent.kind = 'user'
  AND agent.archived_at IS NULL
  AND member.workspace_id = attempt.workspace_id
  AND member.user_id = attempt.actor_user_id
  AND (
      member.role IN ('owner', 'admin')
      OR agent.owner_id = attempt.actor_user_id
  )
RETURNING attempt.*;

-- name: DeleteExpiredAgentEnterpriseIdentityAttempts :execrows
DELETE FROM agent_enterprise_identity_attempt
WHERE expires_at < sqlc.arg('expired_before');
