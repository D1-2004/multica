CREATE INDEX CONCURRENTLY IF NOT EXISTS assoc_edge_dst_rel_idx
    ON assoc_edge (agent_id, dst_type, dst_id, rel, status, last_touched_at DESC);
