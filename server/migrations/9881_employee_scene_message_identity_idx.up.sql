CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_scene_message_identity_idx ON employee_scene_message (workspace_id, agent_id, scene_id, provider_message_id);
