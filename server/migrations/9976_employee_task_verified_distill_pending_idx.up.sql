CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_task_verified_distill_pending_idx ON employee_task_verified_distill (created_at, run_id) WHERE state = 'pending';
