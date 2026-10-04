CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS eval_report_run_idx ON eval_report (workspace_id, run_id);
