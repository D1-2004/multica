CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_scene_message_time_idx ON employee_scene_message (workspace_id, agent_id, scene_id, sent_at, provider_message_id) WHERE withdrawn_at IS NULL;
