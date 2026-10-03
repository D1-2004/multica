CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_input_source_idx ON employee_task_input (workspace_id, agent_id, source_namespace, source_key);
