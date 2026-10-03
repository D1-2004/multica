CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_ready_intent_idx ON employee_task_ready_intent (collection_id, collection_revision);
