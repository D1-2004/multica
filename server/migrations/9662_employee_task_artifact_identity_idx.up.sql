CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_artifact_identity_idx ON employee_task_artifact (workspace_id, queue_task_id, filename, sha256);
