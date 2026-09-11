CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_event_event_batch ON agent_event (batch_id, seq);
