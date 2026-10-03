CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_scene_message_proactive_due_idx ON employee_scene_message (proactive_due_at) WHERE proactive_state = 'pending';
