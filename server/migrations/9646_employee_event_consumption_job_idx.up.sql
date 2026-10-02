CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_event_consumption_job_idx ON employee_event_consumption (workspace_id, agent_id, job_id) WHERE job_id IS NOT NULL;
