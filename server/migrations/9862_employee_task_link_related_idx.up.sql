CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_task_link_related_idx ON employee_task_link (related_task_id, relation);
