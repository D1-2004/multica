CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS assoc_edge_open_unique
    ON assoc_edge (agent_id, src_type, src_id, dst_type, dst_id, rel)
    WHERE status = 'open';
