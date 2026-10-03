CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_scene_message_retention_idx ON employee_scene_message (first_seen_at);
