CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS inbound_coordinator_job_chat_session_idx
    ON inbound_coordinator_job (chat_session_id);
