CREATE INDEX CONCURRENTLY IF NOT EXISTS agent_source_preview_agent_idx ON agent_source_preview(agent_id, applied_at DESC) WHERE applied_at IS NOT NULL;
