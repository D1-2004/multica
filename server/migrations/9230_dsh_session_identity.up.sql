CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS dsh_employee_session_identity ON dsh_employee_session (workspace_id, agent_id, session_id);
