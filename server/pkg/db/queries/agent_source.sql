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
    agent_id, source_type, github_installation_id, repo_owner, repo_name, ref,
    manifest_path, synced_commit_sha, sync_status, last_sync_attempt_at,
    last_synced_at, created_by
) VALUES (
    $1, 'github', $2, $3, $4, $5,
    $6, $7, 'ready', now(), now(), $8
)
RETURNING *;

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
