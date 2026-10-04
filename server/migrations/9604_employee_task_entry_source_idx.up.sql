CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_entry_source_idx ON employee_task_entry (task_id, source_namespace, source_key);
