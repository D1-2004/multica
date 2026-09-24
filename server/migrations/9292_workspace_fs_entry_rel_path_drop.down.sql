CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS workspace_fs_entry_rel_path_idx ON workspace_fs_entry (workspace_id, rel_path);
