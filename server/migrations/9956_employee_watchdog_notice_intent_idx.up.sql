CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_watchdog_notice_intent_idx ON employee_watchdog_notice (episode_id, kind, recipient_key);
