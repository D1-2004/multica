CREATE INDEX CONCURRENTLY IF NOT EXISTS agent_scene_memory_claim_idx
    ON agent_scene_memory (available_at, dirty_since)
    WHERE dirty_revision > flushed_revision;
