CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_event_event_retention ON agent_event (consumed_at) WHERE payload IS NOT NULL AND consumed_at IS NOT NULL;
