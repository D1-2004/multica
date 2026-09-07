CREATE INDEX CONCURRENTLY IF NOT EXISTS response_action_due_idx ON response_action (next_attempt_at, lease_until) WHERE next_attempt_at IS NOT NULL;
