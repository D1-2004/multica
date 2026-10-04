CREATE INDEX CONCURRENTLY IF NOT EXISTS scene_event_receipt_scene_idx ON scene_event_receipt (workspace_id, agent_id, scene_id, created_at);
