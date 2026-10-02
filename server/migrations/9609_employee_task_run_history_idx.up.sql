CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_task_run_history_idx ON employee_task_run (workspace_id, agent_id, task_id, created_at, id);
