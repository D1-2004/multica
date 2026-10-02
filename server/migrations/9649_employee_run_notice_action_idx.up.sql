CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_run_notice_action_idx ON employee_run_notice(action_id) WHERE action_id IS NOT NULL;
