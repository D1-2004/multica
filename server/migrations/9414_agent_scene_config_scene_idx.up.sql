CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS agent_scene_config_scene_idx ON agent_scene_config (agent_id, platform, org_id, scene_key);
