CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_host_notice_scope_idx ON employee_host_notice (workspace_id, agent_id, tenant_org_id, scene_id, principal_id, created_at);
