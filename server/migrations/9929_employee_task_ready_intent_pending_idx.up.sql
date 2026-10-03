CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_task_ready_intent_pending_idx ON employee_task_ready_intent (created_at) WHERE state = 'pending';
