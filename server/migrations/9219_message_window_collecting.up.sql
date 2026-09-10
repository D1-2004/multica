CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_message_window_collecting ON autopilot_message_window(trigger_id) WHERE status='collecting';
