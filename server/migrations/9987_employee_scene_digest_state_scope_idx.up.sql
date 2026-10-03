CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_scene_digest_state_scope_idx ON employee_scene_digest_state (workspace_id, agent_id, scene_id);
