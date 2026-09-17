-- Git-backed agent source ledger

-- name: GetAgentSourceByAgentID :one
SELECT * FROM agent_source
WHERE agent_id = $1;

-- name: GetAgentSourceInWorkspace :one
SELECT agent_source.*
FROM agent_source
JOIN agent ON agent.id = agent_source.agent_id
WHERE agent_source.agent_id = $1
  AND agent.workspace_id = $2
  AND agent.kind = 'user';

-- name: AgentNameExistsInWorkspace :one
SELECT EXISTS (
    SELECT 1
    FROM agent
    WHERE workspace_id = $1
      AND name = $2
      AND kind = 'user'
);

-- name: LockAgentSourceByAgentID :one
SELECT * FROM agent_source
WHERE agent_id = $1
FOR UPDATE;

-- name: CreateAgentSource :one
INSERT INTO agent_source (
    agent_id, workspace_id, source_type, git_connection_id, repo_owner, repo_name, ref,
    repository_url, manifest_path, synced_commit_sha, sync_status, last_sync_attempt_at,
    last_synced_at, created_by
) VALUES (
    $1, $2, 'git', $3, $4, $5, $6,
    sqlc.arg(repository_url), $7, $8, 'ready', now(), now(), $9
)
RETURNING *;

-- name: CreateManagedAgentSource :one
INSERT INTO agent_source (
    agent_id, workspace_id, source_type, managed_source_key, repo_owner, repo_name, repository_url,
    ref, manifest_path, synced_commit_sha, sync_status, last_sync_attempt_at,
    last_synced_at, created_by
) VALUES (
    $1, $2, 'git', $3, $4, $5, 'https://github.com/' || $4 || '/' || $5,
    $6, $7, $8, 'ready', now(), now(), $9
)
RETURNING *;

-- name: GetManagedAgentSourceInWorkspace :one
SELECT * FROM agent_source
WHERE workspace_id = $1
  AND managed_source_key = $2;

-- name: LockManagedAgentOwnerChange :one
-- Lock the new owner's membership before the Agent. Holding the membership
-- KEY SHARE lock through commit prevents a concurrent removal from leaving a
-- managed Agent owned by a non-member. MATERIALIZED makes the lock order
-- deterministic: new-owner member -> Agent.
WITH locked_new_owner AS MATERIALIZED (
    SELECT candidate.workspace_id, candidate.user_id
    FROM member AS candidate
    WHERE candidate.workspace_id = sqlc.arg(workspace_id)
      AND candidate.user_id = sqlc.arg(owner_id)
    FOR KEY SHARE OF candidate
)
SELECT target.*
FROM locked_new_owner AS new_owner
JOIN agent AS target
  ON target.workspace_id = new_owner.workspace_id
WHERE target.id = sqlc.arg(agent_id)
  AND EXISTS (
      SELECT 1
      FROM agent_source
      WHERE agent_source.agent_id = target.id
        AND agent_source.managed_source_key = sqlc.arg(managed_source_key)
  )
FOR UPDATE OF target;

-- name: ApplyManagedAgentOwnerChange :one
-- This must run as a second READ COMMITTED statement after
-- LockManagedAgentOwnerChange. The fresh statement snapshot includes grants
-- committed by transactions that held Agent SHARE while the first statement
-- waited. The transaction still holds the Agent lock, so no new grant can pass
-- admission until this cleanup and owner update commit.
WITH eligible_agent AS MATERIALIZED (
    SELECT target.id, target.owner_id
    FROM agent AS target
    WHERE target.id = sqlc.arg(agent_id)
      AND target.workspace_id = sqlc.arg(workspace_id)
      AND EXISTS (
          SELECT 1
          FROM agent_source
          WHERE agent_source.agent_id = target.id
            AND agent_source.managed_source_key = sqlc.arg(managed_source_key)
      )
), disabled_endpoint AS (
    UPDATE agent_a2a_endpoint AS endpoint
    SET enabled = FALSE,
        updated_at = now()
    FROM eligible_agent AS target
    WHERE endpoint.agent_id = target.id
      AND target.owner_id IS DISTINCT FROM sqlc.arg(owner_id)
    RETURNING endpoint.id
), revoked_clients AS (
    UPDATE a2a_client AS client
    SET status = 'revoked',
        revoked_at = COALESCE(client.revoked_at, now()),
        revoked_by = COALESCE(client.revoked_by, sqlc.arg(owner_id)),
        updated_by = sqlc.arg(owner_id),
        updated_at = now()
    FROM disabled_endpoint AS endpoint
    WHERE client.endpoint_id = endpoint.id
    RETURNING client.id
), revoked_credentials AS (
    UPDATE a2a_client_credential AS credential
    SET status = 'revoked',
        revoked_at = now(),
        revoked_by = sqlc.arg(owner_id),
        updated_at = now()
    -- Referencing revoked_clients fixes the lock order at
    -- agent -> endpoint -> client -> credential.
    WHERE credential.client_id IN (SELECT id FROM revoked_clients)
      AND credential.status = 'active'
    RETURNING credential.id
), ownership_cleanup AS MATERIALIZED (
    SELECT
        (SELECT count(*) FROM revoked_clients) AS revoked_client_count,
        (SELECT count(*) FROM revoked_credentials) AS revoked_credential_count
)
UPDATE agent AS target
SET owner_id = sqlc.arg(owner_id),
    updated_at = now()
FROM eligible_agent AS eligible
CROSS JOIN ownership_cleanup
WHERE target.id = eligible.id
RETURNING target.*;

-- name: ListOutdatedIdleManagedAgentSources :many
SELECT source.*
FROM agent_source AS source
WHERE source.managed_source_key = sqlc.arg(source_key)
  AND source.synced_commit_sha <> sqlc.arg(target_commit_sha)
  AND source.id > sqlc.arg(after_id)
  AND NOT EXISTS (
      SELECT 1
      FROM agent_task_queue AS task
      WHERE task.agent_id = source.agent_id
        AND task.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
  )
ORDER BY source.id ASC
LIMIT sqlc.arg(batch_size);

-- name: AgentHasActiveTasks :one
SELECT EXISTS (
    SELECT 1
    FROM agent_task_queue
    WHERE agent_id = $1
      AND status IN ('queued', 'dispatched', 'running', 'waiting_local_directory', 'deferred')
);

-- name: MarkAgentSourceSyncSucceeded :one
UPDATE agent_source
SET synced_commit_sha = $2,
    sync_status = 'ready',
    last_sync_error = NULL,
    last_sync_attempt_at = now(),
    last_synced_at = now(),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: MarkManagedAgentSourceSyncSucceeded :one
UPDATE agent_source
SET synced_commit_sha = sqlc.arg(synced_commit_sha),
    manifest_path = sqlc.arg(manifest_path),
    sync_status = 'ready',
    last_sync_error = NULL,
    last_sync_attempt_at = now(),
    last_synced_at = now(),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND managed_source_key IS NOT NULL
RETURNING *;

-- name: MarkAgentSourceSyncFailed :one
UPDATE agent_source
SET sync_status = CASE
        WHEN managed_source_key IS NOT NULL THEN 'failed'
        WHEN git_connection_id IS NULL THEN 'disconnected'
        ELSE 'failed'
    END,
    last_sync_error = $2,
    last_sync_attempt_at = now(),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: MarkAgentSourcesDisconnectedByConnection :exec
UPDATE agent_source
SET sync_status = 'disconnected',
    last_sync_error = 'Git connection disconnected',
    last_sync_attempt_at = now(),
    updated_at = now()
WHERE git_connection_id = $1;

-- name: CreateAgentSourceSkill :one
INSERT INTO agent_source_skill (agent_source_id, skill_id, source_path)
VALUES ($1, $2, $3)
ON CONFLICT (agent_source_id, source_path) DO UPDATE SET
    skill_id = EXCLUDED.skill_id,
    updated_at = now()
RETURNING *;

-- name: UpdateAgentSourceSkillPath :one
UPDATE agent_source_skill
SET source_path = sqlc.arg(source_path),
    updated_at = now()
WHERE agent_source_id = sqlc.arg(agent_source_id)
  AND skill_id = sqlc.arg(skill_id)
RETURNING *;

-- name: ListAgentSourceSkills :many
SELECT agent_source_skill.*
FROM agent_source_skill
WHERE agent_source_id = $1
ORDER BY source_path ASC;

-- name: GetAgentSourceSkillBySkillID :one
SELECT agent_source_skill.*
FROM agent_source_skill
WHERE skill_id = $1;

-- name: DeleteAgentSourceSkill :exec
DELETE FROM agent_source_skill
WHERE agent_source_id = $1 AND skill_id = $2;
