CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_webhook_occurrence_queue_idx ON employee_webhook_occurrence (queue_task_id) WHERE queue_task_id IS NOT NULL;
