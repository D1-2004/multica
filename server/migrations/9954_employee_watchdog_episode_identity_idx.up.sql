CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_watchdog_episode_identity_idx ON employee_watchdog_episode (task_id, state_kind, boundary_key, since_at);
