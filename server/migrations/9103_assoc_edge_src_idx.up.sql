CREATE INDEX CONCURRENTLY IF NOT EXISTS assoc_edge_src_idx
    ON assoc_edge (agent_id, src_type, src_id, rel, status);
