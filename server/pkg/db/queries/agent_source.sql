-- GitHub-backed agent source ledger

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
    agent_id, workspace_id, source_type, github_installation_id, repo_owner, repo_name, ref,
    manifest_path, synced_commit_sha, sync_status, last_sync_attempt_at,
    last_synced_at, created_by
) VALUES (
    $1, $2, 'github', $3, $4, $5, $6,
    $7, $8, 'ready', now(), now(), $9
)
RETURNING *;

-- name: CreateManagedAgentSource :one
INSERT INTO agent_source (
    agent_id, workspace_id, source_type, managed_source_key, repo_owner, repo_name,
    ref, manifest_path, synced_commit_sha, sync_status, last_sync_attempt_at,
    last_synced_at, created_by
) VALUES (
    $1, $2, 'managed_git', $3, $4, $5,
    $6, $7, $8, 'ready', now(), now(), $9
)
RETURNING *;

-- name: GetManagedAgentSourceInWorkspace :one
SELECT * FROM agent_source
WHERE workspace_id = $1
  AND managed_source_key = $2;

-- name: UpdateManagedAgentOwner :one
UPDATE agent AS target
SET owner_id = sqlc.arg(owner_id),
    updated_at = now()
WHERE target.id = sqlc.arg(agent_id)
  AND target.workspace_id = sqlc.arg(workspace_id)
  AND EXISTS (
      SELECT 1
      FROM agent_source
      WHERE agent_source.agent_id = target.id
        AND agent_source.source_type = 'managed_git'
        AND agent_source.managed_source_key = sqlc.arg(managed_source_key)
  )
RETURNING target.*;

-- name: ListOutdatedIdleManagedAgentSources :many
SELECT source.*
FROM agent_source AS source
WHERE source.source_type = 'managed_git'
  AND source.managed_source_key = sqlc.arg(source_key)
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

-- name: MarkAgentSourceSyncFailed :one
UPDATE agent_source
SET sync_status = CASE
        WHEN github_installation_id IS NULL THEN 'disconnected'
        ELSE 'failed'
    END,
    last_sync_error = $2,
    last_sync_attempt_at = now(),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: MarkAgentSourcesDisconnectedByInstallation :exec
UPDATE agent_source
SET sync_status = 'disconnected',
    last_sync_error = 'GitHub installation disconnected',
    last_sync_attempt_at = now(),
    updated_at = now()
WHERE github_installation_id = $1;

-- name: CreateAgentSourceSkill :one
INSERT INTO agent_source_skill (agent_source_id, skill_id, source_path)
VALUES ($1, $2, $3)
ON CONFLICT (agent_source_id, source_path) DO UPDATE SET
    skill_id = EXCLUDED.skill_id,
    updated_at = now()
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
