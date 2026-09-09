CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_event_stream_due ON agent_event_stream (due_at) WHERE due_at IS NOT NULL;
