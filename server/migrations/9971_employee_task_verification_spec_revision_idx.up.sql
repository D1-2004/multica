CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_verification_spec_revision_idx ON employee_task_verification_spec (task_id, revision);
