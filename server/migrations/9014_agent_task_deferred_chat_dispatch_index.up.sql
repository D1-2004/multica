-- Serves both halves of the durable channel-chat scheduler:
--   deferred + fire_at <= now()  -> promote the sealed batch to queued
--   queued   + fire_at <= now()  -> retry the post-commit wake/launch handoff
-- While queued, fire_at is the next-notify timestamp. Each delivery claim
-- moves it forward before performing side effects, so a crash only delays the
-- next attempt; dispatched/running tasks naturally leave the index.
--
-- Single-statement migration: CREATE INDEX CONCURRENTLY cannot run inside a
-- transaction or a multi-command string.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_task_queue_channel_dispatch_pending
    ON agent_task_queue (status, fire_at, id)
    WHERE chat_session_id IS NOT NULL
      AND fire_at IS NOT NULL
      AND status IN ('deferred', 'queued');
