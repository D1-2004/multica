CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_first_feedback_identity_idx ON employee_first_feedback (job_id, receipt_id);
