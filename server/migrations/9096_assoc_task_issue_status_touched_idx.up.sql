CREATE INDEX CONCURRENTLY IF NOT EXISTS assoc_task_issue_status_touched_idx
    ON assoc_task (issue_id, status, last_touched_at DESC);
