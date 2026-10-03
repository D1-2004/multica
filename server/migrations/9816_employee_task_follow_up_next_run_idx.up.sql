CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_follow_up_next_run_idx ON employee_task_follow_up (next_run_id) WHERE next_run_id IS NOT NULL;
