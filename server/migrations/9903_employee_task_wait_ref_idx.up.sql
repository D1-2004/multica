CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_wait_ref_idx ON employee_task_wait (task_id, kind, ref_id);
