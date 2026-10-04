CREATE INDEX CONCURRENTLY IF NOT EXISTS assoc_event_agent_scene_idx
    ON assoc_event (agent_id, scene_id, occurred_at DESC)
    WHERE scene_id IS NOT NULL;
