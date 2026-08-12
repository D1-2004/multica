-- name: CreateRunnerPairingSession :one
INSERT INTO runner_pairing_session (
    workspace_id, agent_id, owner_id, pairing_token_hash, expires_at
) VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: BeginRunnerDeviceAuthorization :one
UPDATE runner_pairing_session
SET device_code_hash = $2,
    user_code = $3,
    public_key = $4,
    machine_name = $5,
    os = $6,
    arch = $7,
    client_version = $8,
    roots = $9,
    state = 'device_pending',
    updated_at = now()
WHERE pairing_token_hash = $1
  AND state = 'pending'
  AND expires_at > now()
RETURNING *;

-- name: GetRunnerPairingByDeviceCode :one
SELECT * FROM runner_pairing_session
WHERE device_code_hash = $1
  AND expires_at > now();

-- name: GetRunnerPairingByUserCode :one
SELECT * FROM runner_pairing_session
WHERE user_code = $1
  AND expires_at > now();

-- name: MarkRunnerPairingApproved :one
UPDATE runner_pairing_session
SET state = 'approved', machine_id = $2, approved_at = now(), updated_at = now()
WHERE id = $1 AND state = 'device_pending' AND expires_at > now()
RETURNING *;

-- name: MarkRunnerPairingDenied :one
UPDATE runner_pairing_session
SET state = 'denied', denied_at = now(), updated_at = now()
WHERE id = $1 AND state = 'device_pending' AND expires_at > now()
RETURNING *;

-- name: ConsumeRunnerPairing :one
UPDATE runner_pairing_session
SET state = 'consumed', consumed_at = now(), updated_at = now()
WHERE id = $1 AND state = 'approved' AND expires_at > now()
RETURNING *;

-- name: UpsertRunnerMachine :one
INSERT INTO runner_machine (
    owner_id, name, os, arch, public_key, client_version
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (owner_id, public_key) DO UPDATE
SET name = EXCLUDED.name,
    os = EXCLUDED.os,
    arch = EXCLUDED.arch,
    client_version = EXCLUDED.client_version,
    revoked_at = NULL,
    revoked_by = NULL,
    updated_at = now()
RETURNING *;

-- name: CreateAgentRunnerBinding :one
INSERT INTO agent_runner_binding (
    workspace_id, agent_id, machine_id, bound_by, roots
) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (agent_id, machine_id) WHERE revoked_at IS NULL DO UPDATE
SET roots = EXCLUDED.roots,
    bound_by = EXCLUDED.bound_by,
    updated_at = now()
RETURNING *;

-- name: ListAgentRunnerBindings :many
SELECT
    b.id AS binding_id,
    b.workspace_id,
    b.agent_id,
    b.machine_id,
    b.bound_by,
    b.created_at AS bound_at,
    m.owner_id,
    m.name,
    m.os,
    m.arch,
    m.client_version,
    b.roots,
    m.last_seen_at,
    m.created_at AS machine_created_at
FROM agent_runner_binding b
JOIN runner_machine m ON m.id = b.machine_id
WHERE b.workspace_id = $1
  AND b.agent_id = $2
  AND b.revoked_at IS NULL
  AND m.revoked_at IS NULL
ORDER BY b.created_at DESC;

-- name: GetActiveAgentRunnerBinding :one
SELECT
    b.id AS binding_id,
    b.workspace_id,
    b.agent_id,
    b.machine_id,
    m.owner_id,
    m.name,
    m.os,
    m.arch,
    m.client_version,
    b.roots,
    m.last_seen_at
FROM agent_runner_binding b
JOIN runner_machine m ON m.id = b.machine_id
WHERE b.workspace_id = $1
  AND b.agent_id = $2
  AND b.machine_id = $3
  AND b.revoked_at IS NULL
  AND m.revoked_at IS NULL;

-- name: AgentHasRunnerBindings :one
SELECT EXISTS (
    SELECT 1
    FROM agent_runner_binding b
    JOIN runner_machine m ON m.id = b.machine_id
    WHERE b.agent_id = $1
      AND b.revoked_at IS NULL
      AND m.revoked_at IS NULL
);

-- name: RevokeAgentRunnerBinding :one
UPDATE agent_runner_binding
SET revoked_at = now(), revoked_by = $3, updated_at = now()
WHERE id = $1 AND agent_id = $2 AND revoked_at IS NULL
RETURNING *;

-- name: GetRunnerMachine :one
SELECT * FROM runner_machine WHERE id = $1 AND revoked_at IS NULL;

-- name: UpdateRunnerMachineHeartbeat :one
UPDATE runner_machine
SET last_seen_at = now(), client_version = $2, updated_at = now()
WHERE id = $1 AND revoked_at IS NULL
RETURNING *;

-- name: CreateRunnerAuthChallenge :one
INSERT INTO runner_auth_challenge (machine_id, challenge_hash, expires_at)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetRunnerAuthChallenge :one
SELECT
    c.id,
    c.machine_id,
    c.challenge_hash,
    c.expires_at,
    c.consumed_at,
    m.public_key,
    m.revoked_at AS machine_revoked_at
FROM runner_auth_challenge c
JOIN runner_machine m ON m.id = c.machine_id
WHERE c.id = $1 AND c.machine_id = $2;

-- name: ConsumeRunnerAuthChallenge :one
UPDATE runner_auth_challenge
SET consumed_at = now()
WHERE id = $1
  AND machine_id = $2
  AND challenge_hash = $3
  AND consumed_at IS NULL
  AND expires_at > now()
RETURNING *;

-- name: CreateRunnerCall :one
INSERT INTO runner_call (
    workspace_id, agent_id, task_id, user_id, machine_id,
    tool_name, arguments, roots, expires_at
) SELECT
    sqlc.arg(workspace_id), sqlc.arg(agent_id), sqlc.arg(task_id),
    sqlc.arg(user_id), sqlc.arg(machine_id), sqlc.arg(tool_name),
    sqlc.arg(arguments), b.roots, sqlc.arg(expires_at)
FROM agent_runner_binding b
JOIN runner_machine m ON m.id = b.machine_id
WHERE b.workspace_id = sqlc.arg(workspace_id)
  AND b.agent_id = sqlc.arg(agent_id)
  AND b.machine_id = sqlc.arg(machine_id)
  AND b.revoked_at IS NULL
  AND m.revoked_at IS NULL
FOR SHARE OF b
RETURNING *;

-- name: ExpireRunnerCallsForBinding :many
UPDATE runner_call
SET status = 'expired', error_code = 'runner_binding_revoked',
    error_message = 'The Runner binding was revoked',
    completed_at = now(), updated_at = now()
WHERE agent_id = $1
  AND machine_id = $2
  AND status IN ('queued', 'running')
RETURNING id;

-- name: ClaimRunnerCall :one
UPDATE runner_call
SET status = 'running', started_at = now(), updated_at = now()
WHERE id = $1
  AND machine_id = $2
  AND status = 'queued'
  AND expires_at > now()
RETURNING *;

-- name: RequeueRunnerCall :exec
UPDATE runner_call
SET status = 'queued', started_at = NULL, updated_at = now()
WHERE id = $1 AND machine_id = $2 AND status = 'running';

-- name: ListQueuedRunnerCalls :many
SELECT * FROM runner_call
WHERE machine_id = $1
  AND status = 'queued'
  AND expires_at > now()
ORDER BY created_at
LIMIT 32;

-- name: CompleteRunnerCall :one
UPDATE runner_call
SET status = CASE WHEN sqlc.arg(succeeded)::boolean THEN 'succeeded' ELSE 'failed' END,
    result = sqlc.narg(result),
    error_code = sqlc.narg(error_code),
    error_message = sqlc.narg(error_message),
    completed_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND machine_id = sqlc.arg(machine_id)
  AND status = 'running'
  AND expires_at > now()
RETURNING *;

-- name: GetRunnerCall :one
SELECT * FROM runner_call WHERE id = $1;

-- name: ExpireRunnerCall :exec
UPDATE runner_call
SET status = 'expired', error_code = 'runner_timeout',
    error_message = 'Runner did not finish before the call deadline',
    completed_at = now(), updated_at = now()
WHERE id = $1 AND status IN ('queued', 'running');

-- name: DeleteExpiredRunnerArtifacts :exec
WITH deleted_challenges AS (
    DELETE FROM runner_auth_challenge
    WHERE expires_at < now() - interval '10 minutes'
), deleted_pairings AS (
    DELETE FROM runner_pairing_session
    WHERE expires_at < now() - interval '1 hour'
)
DELETE FROM runner_call
WHERE (completed_at IS NOT NULL AND completed_at < now() - interval '10 minutes')
   OR expires_at < now() - interval '1 hour';
