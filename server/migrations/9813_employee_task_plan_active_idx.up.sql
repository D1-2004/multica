CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_task_plan_active_idx ON employee_task_plan (workspace_id, agent_id, task_id) WHERE state = 'active';
