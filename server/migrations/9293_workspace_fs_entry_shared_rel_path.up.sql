CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS workspace_fs_entry_shared_rel_path_idx ON workspace_fs_entry (workspace_id, rel_path) WHERE agent_id IS NULL;
