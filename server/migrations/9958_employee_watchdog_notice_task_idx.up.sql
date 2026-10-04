CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_watchdog_notice_task_idx ON employee_watchdog_notice (task_id, boundary_key, state);
