CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_watchdog_episode_open_idx ON employee_watchdog_episode (task_id) WHERE state = 'open';
