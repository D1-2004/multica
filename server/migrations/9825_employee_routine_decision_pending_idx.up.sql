CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_routine_decision_pending_idx ON employee_routine_decision (workspace_id, routine_id) WHERE state = 'pending';
