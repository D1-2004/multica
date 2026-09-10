CREATE INDEX CONCURRENTLY IF NOT EXISTS agent_source_preview_workspace_idx ON agent_source_preview(workspace_id, created_by, created_at);
