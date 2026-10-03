CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_scene_job_open_message_idx ON employee_scene_job (workspace_id, agent_id, tenant_org_id, scene_id) WHERE kind = 'message' AND state <> 'completed';
