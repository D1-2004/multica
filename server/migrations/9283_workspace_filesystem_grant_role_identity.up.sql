CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS workspace_filesystem_grant_role_identity_idx ON workspace_filesystem_grant_role (workspace_id, agent_id, generation);
