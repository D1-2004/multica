-- name: CreateLocalAgentSource :one
INSERT INTO agent_source (
    agent_id, workspace_id, source_type, repo_owner, repo_name, ref,
    manifest_path, synced_commit_sha, sync_status, last_synced_at, created_by
) VALUES ($1, $2, 'local', '', '', '', 'agent.json', $3, 'ready', now(), $4)
RETURNING *;


-- name: GetAgentPackageClientMappings :one
SELECT COALESCE((SELECT a2a_client_mappings FROM agent_source WHERE agent_id = $1), '{}'::jsonb)::jsonb;

-- name: UpdateAgentPackageClientMappings :exec
UPDATE agent_source SET a2a_client_mappings = sqlc.arg(mappings) WHERE agent_id = sqlc.arg(agent_id);
