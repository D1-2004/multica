CREATE INDEX CONCURRENTLY IF NOT EXISTS scene_memory_claim_idx
    ON scene_memory (available_at, dirty_since)
    WHERE dirty_revision > flushed_revision;
