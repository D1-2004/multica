-- name: UpsertPlatformTemplateSeed :one
INSERT INTO platform_template_seed (
    system_key, release_version, display_name, description,
    content_hash, bundle_schema_version, bundle_size_bytes, bundle
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (system_key) DO UPDATE SET
    release_version = EXCLUDED.release_version,
    display_name = EXCLUDED.display_name,
    description = EXCLUDED.description,
    content_hash = EXCLUDED.content_hash,
    bundle_schema_version = EXCLUDED.bundle_schema_version,
    bundle_size_bytes = EXCLUDED.bundle_size_bytes,
    bundle = EXCLUDED.bundle,
    updated_at = now()
WHERE platform_template_seed.release_version < EXCLUDED.release_version
RETURNING *;

-- name: GetPlatformTemplateSeed :one
SELECT * FROM platform_template_seed WHERE system_key = $1;

-- name: CreateWorkspaceTemplateFromSeed :one
INSERT INTO agent_template (
    workspace_id, slug, display_name, description, source_type,
    management_mode, system_key, bundle_schema_version,
    bundle_size_bytes, bundle, content_hash, created_by
)
SELECT $1, $2, seed.display_name, seed.description, 'platform',
       'system_managed', seed.system_key, seed.bundle_schema_version,
       seed.bundle_size_bytes, seed.bundle, seed.content_hash, $3
FROM platform_template_seed seed
WHERE seed.system_key = $4
RETURNING *;

-- name: ListAgentTemplatesByWorkspace :many
SELECT t.id, t.workspace_id, t.slug, t.display_name, t.description,
       t.source_type, t.management_mode, t.system_key, t.content_hash,
       t.bundle_size_bytes, t.created_by, t.created_at, t.updated_at,
       s.github_installation_id, s.repo_owner, s.repo_name, s.ref,
       s.synced_commit_sha, s.sync_status, s.last_sync_error,
       s.last_sync_attempt_at, s.last_synced_at
FROM agent_template t
LEFT JOIN agent_template_github_source s ON s.template_id = t.id
WHERE t.workspace_id = $1
ORDER BY t.created_at ASC, t.slug ASC;

-- name: GetAgentTemplateByWorkspaceAndSlug :one
SELECT * FROM agent_template
WHERE workspace_id = $1 AND slug = $2;

-- name: CreateGitHubAgentTemplate :one
INSERT INTO agent_template (
    workspace_id, slug, display_name, description, source_type,
    management_mode, bundle_schema_version, bundle_size_bytes,
    bundle, content_hash, created_by
) VALUES ($1, $2, $3, $4, 'github', 'workspace_managed', $5, $6, $7, $8, $9)
RETURNING *;

-- name: CreateAgentTemplateGitHubSource :one
INSERT INTO agent_template_github_source (
    template_id, workspace_id, github_installation_id, repo_owner,
    repo_name, ref, synced_commit_sha
) VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetAgentTemplateGitHubSource :one
SELECT * FROM agent_template_github_source
WHERE workspace_id = $1 AND template_id = $2;

-- name: LockAgentTemplateGitHubSource :one
SELECT * FROM agent_template_github_source
WHERE workspace_id = $1 AND template_id = $2
FOR UPDATE;

-- name: ReplaceGitHubAgentTemplateSnapshot :one
UPDATE agent_template SET
    display_name = $3,
    description = $4,
    bundle_schema_version = $5,
    bundle_size_bytes = $6,
    bundle = $7,
    content_hash = $8,
    updated_at = now()
WHERE workspace_id = $1 AND id = $2
  AND source_type = 'github' AND management_mode = 'workspace_managed'
RETURNING *;

-- name: MarkAgentTemplateSyncSucceeded :one
UPDATE agent_template_github_source SET
    synced_commit_sha = $3,
    sync_status = 'ready',
    last_sync_error = NULL,
    last_sync_attempt_at = now(),
    last_synced_at = now(),
    updated_at = now()
WHERE workspace_id = $1 AND template_id = $2
RETURNING *;

-- name: MarkAgentTemplateSyncFailed :one
UPDATE agent_template_github_source SET
    sync_status = 'failed',
    last_sync_error = $3,
    last_sync_attempt_at = now(),
    updated_at = now()
WHERE workspace_id = $1 AND template_id = $2
RETURNING *;

-- name: DeleteWorkspaceManagedAgentTemplate :execrows
DELETE FROM agent_template
WHERE workspace_id = $1 AND slug = $2
  AND source_type = 'github' AND management_mode = 'workspace_managed';
