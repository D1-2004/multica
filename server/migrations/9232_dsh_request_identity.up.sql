CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS dsh_task_binding_request ON dsh_task_binding (workspace_id, agent_id, request_id);
