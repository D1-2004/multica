CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_event_route_source ON agent_event_route (target_identity, source_id, agent_id);
