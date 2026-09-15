CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS git_connection_github_identity_idx ON git_connection (workspace_id, installation_id);
