CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_plan_revision_idx ON employee_task_plan (task_id, plan_revision);
