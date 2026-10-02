CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_task_artifact_run_idx ON employee_task_artifact (workspace_id, agent_id, tenant_org_id, task_id, run_id, created_at) WHERE state='ready';
