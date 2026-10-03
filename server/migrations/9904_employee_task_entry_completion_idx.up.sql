CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_entry_completion_idx ON employee_task_entry (task_id, goal_revision) WHERE kind = 'goal_completed';
