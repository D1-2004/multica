CREATE INDEX CONCURRENTLY IF NOT EXISTS coordinator_user_decision_due_idx ON coordinator_user_decision (state, available_at);
