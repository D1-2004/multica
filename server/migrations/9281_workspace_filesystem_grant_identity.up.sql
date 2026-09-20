CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS workspace_filesystem_grant_identity_idx ON workspace_filesystem_grant (workspace_id, agent_id);
