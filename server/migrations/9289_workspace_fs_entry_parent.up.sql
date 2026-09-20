CREATE INDEX CONCURRENTLY IF NOT EXISTS workspace_fs_entry_parent_idx ON workspace_fs_entry (workspace_id, parent_path);
