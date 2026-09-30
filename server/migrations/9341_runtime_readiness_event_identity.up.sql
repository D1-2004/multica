CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS runtime_readiness_event_identity ON runtime_readiness_event (workspace_id, agent_id);
