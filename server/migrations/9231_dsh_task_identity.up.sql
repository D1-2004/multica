CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS dsh_task_binding_identity ON dsh_task_binding (workspace_id, agent_id, task_id);
