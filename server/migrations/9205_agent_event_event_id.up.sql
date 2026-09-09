CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_event_event_id ON agent_event (stream_id, event_id);
