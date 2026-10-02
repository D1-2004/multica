CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS scene_event_receipt_source_idx ON scene_event_receipt (workspace_id, agent_id, source, source_event_id);
