CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_scene_job_window_idx ON employee_scene_job (workspace_id, agent_id, tenant_org_id, scene_id, principal_id, created_at) WHERE state='pending';
