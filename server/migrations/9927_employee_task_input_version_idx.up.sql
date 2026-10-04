CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_input_version_idx ON employee_task_input (collection_id, invitation_id, version);
