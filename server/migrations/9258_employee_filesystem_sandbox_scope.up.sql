CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_filesystem_sandbox_scope_idx ON employee_filesystem_sandbox (workspace_id, agent_id, scope_id);
