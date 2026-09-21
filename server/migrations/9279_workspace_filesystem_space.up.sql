CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS workspace_filesystem_space_idx ON workspace_filesystem (file_system_id, space_id);
