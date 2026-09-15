CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS dsh_schedule_identity ON dsh_schedule (workspace_id, agent_id, session_id, schedule_id);
