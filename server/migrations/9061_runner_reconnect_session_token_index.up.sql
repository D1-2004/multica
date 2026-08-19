CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_runner_reconnect_session_token_hash
    ON runner_reconnect_session(token_hash);
