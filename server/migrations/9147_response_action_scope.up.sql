CREATE INDEX CONCURRENTLY IF NOT EXISTS response_action_scope_idx ON response_action (workspace_id, agent_id, request_id);
