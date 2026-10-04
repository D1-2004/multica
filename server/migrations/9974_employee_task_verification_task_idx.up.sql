CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_task_verification_task_idx ON employee_task_verification (workspace_id, task_id, goal_revision, completed_at);
