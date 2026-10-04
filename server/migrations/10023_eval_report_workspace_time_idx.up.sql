CREATE INDEX CONCURRENTLY IF NOT EXISTS eval_report_workspace_time_idx ON eval_report (workspace_id, received_at DESC, id DESC);
