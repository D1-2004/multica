CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS assoc_person_agent_key_idx
    ON assoc_person (agent_id, person_key);
