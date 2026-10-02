CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_learning_replay_idx ON employee_learning (workspace_id, agent_id, tenant_org_id, scene_id, scope_kind, principal_id, replay_key);
