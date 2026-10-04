CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_task_entry_seq_idx ON employee_task_entry (task_id, seq);
