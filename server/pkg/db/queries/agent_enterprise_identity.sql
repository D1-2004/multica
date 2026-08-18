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

-- name: GetReusableAgentEnterpriseIdentitySource :one
SELECT *
FROM agent_enterprise_identity
WHERE workspace_id = sqlc.arg('workspace_id')
  AND bound_by = sqlc.arg('bound_by')
  AND raw_emp_id = sqlc.arg('raw_emp_id')
  AND buc_agent_id = sqlc.arg('buc_agent_id')
  AND buc_identity_source_runtime_id = ANY(sqlc.arg('runtime_ids')::uuid[])
  AND buc_identity_source_sandbox_id IS NOT NULL
  AND btrim(buc_identity_source_sandbox_id) <> ''
  AND buc_identity_source_updated_at IS NOT NULL
  AND status = 'active'
ORDER BY buc_identity_source_updated_at DESC, id
LIMIT 1;

-- name: ListActiveAgentEnterpriseIdentitySourceReferences :many
SELECT *
FROM agent_enterprise_identity
WHERE workspace_id = sqlc.arg('workspace_id')
  AND buc_identity_source_runtime_id = sqlc.arg('runtime_id')
  AND buc_identity_source_sandbox_id = sqlc.arg('sandbox_id')
  AND status = 'active'
ORDER BY id;

-- name: CountActiveAgentEnterpriseIdentitySourceReferences :one
SELECT count(*)
FROM agent_enterprise_identity
WHERE workspace_id = sqlc.arg('workspace_id')
  AND buc_identity_source_runtime_id = sqlc.arg('runtime_id')
  AND buc_identity_source_sandbox_id = sqlc.arg('sandbox_id')
  AND status = 'active';

-- name: ListActiveAgentEnterpriseIdentitiesByRuntime :many
SELECT identity.*
FROM agent_enterprise_identity AS identity
JOIN agent
  ON agent.id = identity.agent_id
 AND agent.workspace_id = identity.workspace_id
WHERE identity.status = 'active'
  AND (
    identity.buc_identity_source_runtime_id = sqlc.arg('runtime_id')
    OR agent.runtime_id = sqlc.arg('runtime_id')
  )
ORDER BY identity.id;

-- name: UpsertAgentEnterpriseIdentity :one
INSERT INTO agent_enterprise_identity (
    workspace_id,
    agent_id,
    raw_emp_id,
    display_name,
    buc_agent_id,
    agent_spiffe_id,
    aip_id,
    buc_identity_source_sandbox_id,
    buc_identity_source_runtime_id,
    buc_identity_source_updated_at,
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
    sqlc.arg('buc_identity_source_sandbox_id'),
    sqlc.arg('buc_identity_source_runtime_id'),
    now(),
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
    buc_identity_source_sandbox_id = EXCLUDED.buc_identity_source_sandbox_id,
    buc_identity_source_runtime_id = EXCLUDED.buc_identity_source_runtime_id,
    buc_identity_source_updated_at = EXCLUDED.buc_identity_source_updated_at,
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
    buc_identity_source_sandbox_id = NULL,
    buc_identity_source_runtime_id = NULL,
    buc_identity_source_updated_at = NULL,
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
    buc_identity_source_sandbox_id = NULL,
    buc_identity_source_runtime_id = NULL,
    buc_identity_source_updated_at = NULL,
    authx_refresh_token_encrypted = NULL,
    authx_refresh_expires_at = NULL,
    token_version = token_version + 1,
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND token_version = sqlc.arg('expected_token_version')
  AND status = 'active';

-- name: MarkAgentEnterpriseIdentitiesNeedsReauthByRuntime :execrows
UPDATE agent_enterprise_identity AS identity
SET status = 'needs_reauth',
    buc_identity_source_sandbox_id = NULL,
    buc_identity_source_runtime_id = NULL,
    buc_identity_source_updated_at = NULL,
    authx_refresh_token_encrypted = NULL,
    authx_refresh_expires_at = NULL,
    token_version = identity.token_version + 1,
    updated_at = now()
FROM agent
WHERE identity.agent_id = agent.id
  AND identity.workspace_id = agent.workspace_id
  AND identity.status = 'active'
  AND (
    identity.buc_identity_source_runtime_id = sqlc.arg('runtime_id')
    OR agent.runtime_id = sqlc.arg('runtime_id')
  );

-- name: ListAgentEnterpriseIdentitiesForMaintenance :many
SELECT *
FROM agent_enterprise_identity
WHERE status = 'active'
  AND (
    authx_refresh_expires_at <= sqlc.arg('rotate_before')
    OR buc_identity_source_updated_at <= sqlc.arg('source_check_before')
  )
ORDER BY LEAST(authx_refresh_expires_at, buc_identity_source_updated_at), id
LIMIT sqlc.arg('batch_size');

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

-- name: CompareAndSwapAgentEnterpriseIdentitySource :one
UPDATE agent_enterprise_identity
SET buc_identity_source_sandbox_id = sqlc.arg('buc_identity_source_sandbox_id'),
    buc_identity_source_updated_at = now(),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND buc_identity_source_sandbox_id = sqlc.arg('expected_source_sandbox_id')
  AND buc_identity_source_runtime_id = sqlc.arg('expected_source_runtime_id')
  AND status = 'active'
RETURNING *;

-- name: TouchActiveAgentEnterpriseIdentitySourceReferences :execrows
UPDATE agent_enterprise_identity
SET buc_identity_source_updated_at = now(),
    updated_at = now()
WHERE workspace_id = sqlc.arg('workspace_id')
  AND buc_identity_source_runtime_id = sqlc.arg('runtime_id')
  AND buc_identity_source_sandbox_id = sqlc.arg('sandbox_id')
  AND status = 'active';

-- name: MarkAgentEnterpriseIdentitiesNeedsReauthBySource :many
UPDATE agent_enterprise_identity
SET status = 'needs_reauth',
    buc_identity_source_sandbox_id = NULL,
    buc_identity_source_runtime_id = NULL,
    buc_identity_source_updated_at = NULL,
    authx_refresh_token_encrypted = NULL,
    authx_refresh_expires_at = NULL,
    token_version = token_version + 1,
    updated_at = now()
WHERE workspace_id = sqlc.arg('workspace_id')
  AND buc_identity_source_runtime_id = sqlc.arg('runtime_id')
  AND buc_identity_source_sandbox_id = sqlc.arg('sandbox_id')
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
    sqlc.narg('requested_raw_emp_id'),
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
SET consumed_at = now(),
    completion_status = 'pending',
    completion_error_code = NULL,
    completed_at = NULL
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

-- name: GetActiveAgentEnterpriseIdentityAttempt :one
SELECT *
FROM agent_enterprise_identity_attempt
WHERE workspace_id = sqlc.arg('workspace_id')
  AND agent_id = sqlc.arg('agent_id')
  AND consumed_at IS NOT NULL
  AND completion_status = 'pending'
  AND consumed_at > sqlc.arg('active_after')
ORDER BY consumed_at DESC
LIMIT 1;

-- name: GetAgentEnterpriseIdentityAttemptStatus :one
SELECT
    id,
    completion_status,
    completion_error_code,
    completed_at
FROM agent_enterprise_identity_attempt
WHERE id = sqlc.arg('id')
  AND workspace_id = sqlc.arg('workspace_id')
  AND agent_id = sqlc.arg('agent_id')
  AND consumed_at IS NOT NULL;

-- name: CompleteAgentEnterpriseIdentityAttempt :execrows
UPDATE agent_enterprise_identity_attempt
SET completion_status = sqlc.arg('completion_status'),
    completion_error_code = sqlc.narg('completion_error_code'),
    completed_at = now()
WHERE id = sqlc.arg('id')
  AND completion_status = 'pending';

-- name: DeleteExpiredAgentEnterpriseIdentityAttempts :execrows
DELETE FROM agent_enterprise_identity_attempt
WHERE expires_at < sqlc.arg('expired_before');
