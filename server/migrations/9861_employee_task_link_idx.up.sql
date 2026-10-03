CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_link_idx ON employee_task_link (task_id, relation, related_task_id);
