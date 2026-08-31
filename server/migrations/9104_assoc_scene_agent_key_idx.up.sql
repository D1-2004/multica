CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS assoc_scene_agent_key_idx
    ON assoc_scene (agent_id, scene_key);
