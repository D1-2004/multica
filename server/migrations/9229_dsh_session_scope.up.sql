CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS dsh_employee_session_scope ON dsh_employee_session (workspace_id, agent_id, scope_kind, scope_id);
