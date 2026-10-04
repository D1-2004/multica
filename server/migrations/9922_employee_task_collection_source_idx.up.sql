CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_collection_source_idx ON employee_task_collection (task_id, source_namespace, source_key);
