-- This intentionally fails instead of discarding data if multiple deferred A2A
-- turns already share a Chat Session. Drain or resume those turns before a
-- schema rollback.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_agent_task_queue_deferred_chat_session
    ON agent_task_queue (chat_session_id)
    WHERE status = 'deferred' AND chat_session_id IS NOT NULL;
