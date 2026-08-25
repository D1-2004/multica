CREATE INDEX CONCURRENTLY IF NOT EXISTS agent_okr_agent_idx ON agent_okr (agent_id, position, created_at);
