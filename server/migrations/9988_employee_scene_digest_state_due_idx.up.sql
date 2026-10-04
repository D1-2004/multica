CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_scene_digest_state_due_idx ON employee_scene_digest_state (due_at) WHERE blocked_at IS NULL AND pending_human > 0;
