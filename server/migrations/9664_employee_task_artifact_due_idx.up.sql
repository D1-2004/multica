CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_task_artifact_due_idx ON employee_task_artifact (next_attempt_at, attachment_id) WHERE state<>'ready';
