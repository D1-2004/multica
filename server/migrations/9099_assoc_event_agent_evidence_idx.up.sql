CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS assoc_event_agent_evidence_idx
    ON assoc_event (workspace_id, agent_id, evidence_id);
