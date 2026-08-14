CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_runner_reconnect_session_expiry
    ON runner_reconnect_session(expires_at)
    WHERE consumed_at IS NULL;
