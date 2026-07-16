-- A channel chat keeps exactly one open debounce batch per chat session.
-- Existing comment-routing deferred tasks have chat_session_id IS NULL and
-- are deliberately outside this index, so their fan-out semantics are
-- unchanged.
--
-- Single-statement migration: CREATE INDEX CONCURRENTLY cannot run inside a
-- transaction or a multi-command string.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_agent_task_queue_deferred_chat_session
    ON agent_task_queue (chat_session_id)
    WHERE status = 'deferred' AND chat_session_id IS NOT NULL;
