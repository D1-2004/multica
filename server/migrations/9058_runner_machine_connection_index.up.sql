CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_runner_machine_connection
    ON runner_machine(connection_id)
    WHERE revoked_at IS NULL;
