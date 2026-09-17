-- name: CreateAgentSourcePreview :one
WITH locked_workspace AS MATERIALIZED (
    SELECT workspace.id
    FROM workspace
    WHERE workspace.id = sqlc.arg(workspace_id)
    FOR KEY SHARE
)
INSERT INTO agent_source_preview (
    workspace_id, created_by, agent_id, agent_source_id, git_connection_id,
    repository, ref, resolved_sha, expected_source_sha, expected_state_hash, snapshot
) SELECT locked_workspace.id, sqlc.arg(created_by), sqlc.narg(agent_id), sqlc.narg(agent_source_id),
    sqlc.arg(git_connection_id), sqlc.arg(repository), sqlc.arg(ref), sqlc.arg(resolved_sha),
    sqlc.arg(expected_source_sha), sqlc.arg(expected_state_hash), sqlc.arg(snapshot)
FROM locked_workspace
RETURNING *;

-- name: GetAgentSourceByID :one
SELECT * FROM agent_source WHERE id = $1;

-- name: GetAgentSourcePreview :one
SELECT * FROM agent_source_preview
WHERE id = $1 AND workspace_id = $2 AND created_by = $3;

-- name: LockAgentSourcePreview :one
SELECT * FROM agent_source_preview
WHERE id = $1 AND workspace_id = $2 AND created_by = $3
FOR UPDATE;

-- name: MarkAgentSourcePreviewApplied :one
UPDATE agent_source_preview
SET agent_id = $2, applied_at = clock_timestamp(), applied_source = $3, applied_changed = $4,
    snapshot = sqlc.arg(snapshot)
WHERE id = $1 AND applied_at IS NULL
RETURNING *;

-- name: GetAgentPublication :one
SELECT * FROM agent_source_preview
WHERE id = $1 AND agent_id = $2 AND workspace_id = $3 AND applied_at IS NOT NULL;

-- name: ListAgentPublications :many
SELECT publication.id, publication.ref, publication.resolved_sha,
    publication.repository, publication.git_connection_id,
    publication.applied_at, publication.created_by, publication.applied_changed,
    COALESCE(author.name, '')::text AS author_name,
    COALESCE(publication.snapshot->>'rollback_of', '')::text AS rollback_of,
    (publication.snapshot ? 'published_definition')::boolean AS has_configuration_snapshot,
    publication.expected_state_hash = '' AS initial_publication
FROM agent_source_preview AS publication
LEFT JOIN "user" AS author ON author.id = publication.created_by
WHERE publication.agent_id = sqlc.arg(agent_id)
  AND publication.workspace_id = sqlc.arg(workspace_id)
  AND publication.applied_at IS NOT NULL
  AND (sqlc.narg(before_id)::uuid IS NULL OR (publication.applied_at, publication.id) < (
      SELECT cursor.applied_at, cursor.id FROM agent_source_preview AS cursor
      WHERE cursor.id = sqlc.narg(before_id)
        AND cursor.agent_id = sqlc.arg(agent_id) AND cursor.workspace_id = sqlc.arg(workspace_id)
        AND cursor.applied_at IS NOT NULL
  ))
ORDER BY publication.applied_at DESC, publication.id DESC
LIMIT 51;

-- name: LatestAppliedAgentSourcePreview :one
SELECT * FROM agent_source_preview
WHERE agent_id = $1 AND workspace_id = $2 AND applied_at IS NOT NULL
ORDER BY applied_at DESC, id DESC
LIMIT 1;

-- name: DeleteExpiredAgentSourcePreviews :exec
DELETE FROM agent_source_preview
WHERE workspace_id = $1 AND created_by = $2 AND applied_at IS NULL AND expires_at < now();

-- name: DeleteAgentSourcePreviewsByWorkspace :exec
DELETE FROM agent_source_preview WHERE workspace_id = $1;

-- name: MarkAgentSourceBranchSyncSucceeded :one
UPDATE agent_source
SET ref = $2, synced_commit_sha = $3, manifest_path = $4, sync_status = 'ready',
    last_sync_error = NULL, last_sync_attempt_at = now(),
    last_synced_at = now(), updated_at = now()
WHERE id = $1
RETURNING *;

-- name: LockSourceSkills :many
SELECT skill.* FROM skill
JOIN agent_source_skill AS mapping ON mapping.skill_id = skill.id
WHERE mapping.agent_source_id = $1
ORDER BY skill.id
FOR UPDATE OF skill;

-- name: LockSourceSkillAssignments :many
SELECT agent_skill.* FROM agent_skill
JOIN agent_source_skill AS mapping ON mapping.skill_id = agent_skill.skill_id
WHERE mapping.agent_source_id = $1
ORDER BY agent_skill.skill_id
FOR UPDATE OF agent_skill;

-- name: DeleteSkillDependents :exec
WITH removed_files AS (
    DELETE FROM skill_file WHERE skill_file.skill_id = $1
), removed_assignments AS (
    DELETE FROM agent_skill WHERE agent_skill.skill_id = $1
), removed_source_mapping AS (
    DELETE FROM agent_source_skill WHERE agent_source_skill.skill_id = $1
)
DELETE FROM skill_to_label WHERE skill_to_label.skill_id = $1;
