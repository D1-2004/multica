CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS dsh_employee_host_identity ON dsh_employee_host (workspace_id, agent_id);
