-- name: GetChatContextResetBoundary :one
-- A fresh provider session must also exclude older database-backed context.
-- Keep the visible transcript intact; constrain only the model's history.
-- Use the immutable input owner so retries preserve the same reset boundary.
SELECT input.id, input.created_at
FROM agent_task_queue task
JOIN chat_message input
  ON input.task_id = COALESCE(task.chat_input_task_id, task.id)
  AND input.chat_session_id = task.chat_session_id
  AND input.role = 'user'
WHERE task.chat_session_id = sqlc.arg('chat_session_id')
  AND task.force_fresh_session = TRUE
  AND NOT EXISTS (
      SELECT 1 FROM chat_message earlier
      WHERE earlier.task_id = input.task_id AND earlier.role = 'user'
        AND (earlier.created_at, earlier.id) < (input.created_at, input.id)
  )
  AND (input.created_at, input.id) <= (sqlc.arg('input_created_at')::timestamptz, sqlc.arg('input_id')::uuid)
ORDER BY input.created_at DESC, input.id DESC
LIMIT 1;
