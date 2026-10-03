CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_verification_attempt_run_idx ON employee_task_verification_attempt (run_id, spec_digest);
