CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_follow_up_run_idx ON employee_task_follow_up (task_id, plan_revision, run_id);
