CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_event_stream_resource ON agent_event_stream (workspace_id, agent_id, source_key, resource_key);
