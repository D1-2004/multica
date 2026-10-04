CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_issue_idx ON employee_task (workspace_id, agent_id, issue_id) WHERE issue_id IS NOT NULL;
