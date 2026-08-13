-- The replacement channel-only index from 9055 must exist before this runs.
-- Removing the broad index allows several deferred A2A turns to share one Chat
-- Session while preserving channel debounce coalescing.
--
-- Single-statement migration: DROP INDEX CONCURRENTLY cannot run inside a
-- transaction or a multi-command string.
DROP INDEX CONCURRENTLY IF EXISTS uq_agent_task_queue_deferred_chat_session;
