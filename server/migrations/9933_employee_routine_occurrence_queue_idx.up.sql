CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_routine_occurrence_queue_idx ON employee_routine_occurrence (queue_task_id) WHERE queue_task_id IS NOT NULL;
