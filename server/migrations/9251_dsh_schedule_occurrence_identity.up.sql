CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS dsh_schedule_occurrence_identity ON dsh_schedule_occurrence (workspace_id, agent_id, session_id, schedule_id, occurrence_at);
