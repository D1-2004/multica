CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS coordinator_user_decision_event_idx ON coordinator_user_decision_event (environment, event_id);
