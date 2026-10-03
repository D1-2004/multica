CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_verified_distill_identity_idx ON employee_task_verified_distill (run_id, spec_digest);
