CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS scene_memory_identity_idx
    ON scene_memory (workspace_id, agent_id, platform, org_id, scene_key);
