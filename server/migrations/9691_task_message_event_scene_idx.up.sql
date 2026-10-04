CREATE INDEX CONCURRENTLY IF NOT EXISTS task_message_event_scene_idx ON task_message ((event->>'workspace_id'), (event->>'scene_id'), task_id, seq) WHERE event IS NOT NULL;
