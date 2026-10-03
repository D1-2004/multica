CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_watchdog_notice_action_idx ON employee_watchdog_notice (action_id) WHERE action_id IS NOT NULL;
