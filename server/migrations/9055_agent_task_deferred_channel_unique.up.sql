-- Channel message batching needs one open debounce row per Chat Session, while
-- A2A needs multiple deferred turns in that same session. fire_at is the
-- durable channel-batch marker; A2A turns deliberately leave it NULL.
--
-- Create the replacement before removing the historical broader index so a
-- frozen rolling deployment never leaves channel batching without a unique
-- arbiter.
--
-- Single-statement migration: CREATE INDEX CONCURRENTLY cannot run inside a
-- transaction or a multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_agent_task_queue_deferred_channel_session
    ON agent_task_queue (chat_session_id)
    WHERE status = 'deferred'
      AND chat_session_id IS NOT NULL
      AND fire_at IS NOT NULL;
