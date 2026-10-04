CREATE INDEX CONCURRENTLY employee_progress_report_task_idx ON employee_progress_report (workspace_id, agent_id, tenant_org_id, scene_id, task_id, created_at DESC);
