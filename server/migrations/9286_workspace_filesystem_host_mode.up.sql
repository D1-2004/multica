CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS workspace_filesystem_host_mode_idx ON workspace_filesystem_host (workspace_id, mode);
