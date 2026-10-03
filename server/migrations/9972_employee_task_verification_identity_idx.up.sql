CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_verification_identity_idx ON employee_task_verification (run_id, check_id, evidence_ref);
