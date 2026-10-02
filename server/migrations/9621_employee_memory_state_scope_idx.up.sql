CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_memory_state_scope_idx ON employee_memory_state (workspace_id, agent_id, tenant_org_id, scene_id, scope_kind, principal_id);
