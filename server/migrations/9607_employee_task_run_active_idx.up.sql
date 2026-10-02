CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_run_active_idx ON employee_task_run (task_id) WHERE state = 'running';
