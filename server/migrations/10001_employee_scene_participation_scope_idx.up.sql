CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_scene_participation_scope_idx ON employee_scene_participation (workspace_id, agent_id, tenant_org_id, scene_id);
