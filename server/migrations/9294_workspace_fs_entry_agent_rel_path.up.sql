CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS workspace_fs_entry_agent_rel_path_idx ON workspace_fs_entry (workspace_id, agent_id, rel_path) WHERE agent_id IS NOT NULL;
