CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_message_window_pending ON autopilot_message_window(next_attempt_at,due_at) WHERE status IN ('collecting','ready');
