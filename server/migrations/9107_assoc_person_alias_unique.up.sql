CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS assoc_person_alias_unique
    ON assoc_person_alias (agent_id, alias_key);
