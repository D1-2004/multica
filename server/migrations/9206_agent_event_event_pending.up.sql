CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_event_event_pending ON agent_event (stream_id, seq) WHERE consumed_at IS NULL;
