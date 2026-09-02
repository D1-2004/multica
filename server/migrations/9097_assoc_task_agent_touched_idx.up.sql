CREATE INDEX CONCURRENTLY IF NOT EXISTS assoc_task_agent_touched_idx
    ON assoc_task (agent_id, last_touched_at DESC)
    WHERE status IN ('open', 'waiting');
