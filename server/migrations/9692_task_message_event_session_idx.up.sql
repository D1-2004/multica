CREATE INDEX CONCURRENTLY IF NOT EXISTS task_message_event_session_idx ON task_message ((event->>'workspace_id'), (event->'source'->>'session_id'), task_id, seq) WHERE event IS NOT NULL;
