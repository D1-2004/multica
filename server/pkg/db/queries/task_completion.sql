-- =====================
-- Task completion outbox
-- =====================

-- name: GetTaskCompletionTarget :one
WITH RECURSIVE lineage AS (
    SELECT task.id, task.parent_task_id, task.context, 0 AS depth
    FROM agent_task_queue task
    WHERE task.id = $1

    UNION ALL

    SELECT parent.id, parent.parent_task_id, parent.context, child.depth + 1
    FROM agent_task_queue parent
    JOIN lineage child ON parent.id = child.parent_task_id
)
SELECT
    lineage.id AS root_task_id,
    COALESCE(lineage.context #>> '{completion_callback,url}', '')::text AS callback_url,
    COALESCE(lineage.context #>> '{completion_callback,target}', '')::text AS target_identity
FROM lineage
WHERE lineage.parent_task_id IS NULL
  AND COALESCE(lineage.context #>> '{completion_callback,url}', '') <> ''
  AND COALESCE(lineage.context #>> '{completion_callback,target}', '') <> ''
ORDER BY lineage.depth DESC
LIMIT 1;

-- name: EnqueueTaskCompletion :one
INSERT INTO task_completion_outbox AS existing (
    root_task_id,
    terminal_task_id,
    callback_url,
    target_identity,
    request_id,
    agent_id,
    external_session_id,
    execution_status,
    result_message,
    error,
    failure_reason
) VALUES (
    $1, $2, $3, $4, $5, $6,
    sqlc.narg('external_session_id'),
    $7, $8,
    sqlc.narg('error'),
    sqlc.narg('failure_reason')
)
ON CONFLICT (root_task_id) DO UPDATE
SET updated_at = existing.updated_at
WHERE existing.terminal_task_id = EXCLUDED.terminal_task_id
  AND existing.callback_url = EXCLUDED.callback_url
  AND existing.target_identity = EXCLUDED.target_identity
  AND existing.request_id = EXCLUDED.request_id
  AND existing.agent_id = EXCLUDED.agent_id
  AND existing.external_session_id IS NOT DISTINCT FROM EXCLUDED.external_session_id
  AND existing.execution_status = EXCLUDED.execution_status
  AND existing.result_message = EXCLUDED.result_message
  AND existing.error IS NOT DISTINCT FROM EXCLUDED.error
  AND existing.failure_reason IS NOT DISTINCT FROM EXCLUDED.failure_reason
RETURNING *;

-- name: EnqueueSynchronousTaskCompletion :one
INSERT INTO task_completion_outbox AS existing (
    root_task_id,
    terminal_task_id,
    callback_url,
    target_identity,
    request_id,
    agent_id,
    execution_status,
    result_message,
    error,
    failure_reason
) VALUES (
    NULL, NULL, $1, $2, $3, $4, 'failed', '', $5, $6
)
ON CONFLICT (request_id) DO UPDATE
SET updated_at = existing.updated_at
WHERE existing.root_task_id IS NULL
  AND existing.terminal_task_id IS NULL
  AND existing.callback_url = EXCLUDED.callback_url
  AND existing.target_identity = EXCLUDED.target_identity
  AND existing.agent_id = EXCLUDED.agent_id
  AND existing.execution_status = EXCLUDED.execution_status
  AND existing.result_message = EXCLUDED.result_message
  AND existing.error IS NOT DISTINCT FROM EXCLUDED.error
  AND existing.failure_reason IS NOT DISTINCT FROM EXCLUDED.failure_reason
RETURNING *;

-- name: GetTaskCompletionByRequestID :one
SELECT *
FROM task_completion_outbox
WHERE request_id = @request_id;

-- name: ClaimTaskCompletion :one
WITH candidate AS (
    SELECT id
    FROM task_completion_outbox queued
    WHERE queued.status = 'queued'
      AND queued.target_identity = sqlc.arg('worker_target_identity')
      AND queued.available_at <= now()
      AND (queued.lease_expires_at IS NULL OR queued.lease_expires_at <= now())
    ORDER BY queued.available_at, queued.created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE task_completion_outbox AS completion
SET lease_token = gen_random_uuid(),
    lease_expires_at = now() + interval '2 minutes',
    updated_at = now()
FROM candidate
WHERE completion.id = candidate.id
RETURNING completion.*;

-- name: CompleteTaskCompletion :one
UPDATE task_completion_outbox
SET status = 'delivered',
    attempt_count = attempt_count + 1,
    lease_token = NULL,
    lease_expires_at = NULL,
    delivered_at = now(),
    updated_at = now()
WHERE id = $1
  AND lease_token = $2
  AND status = 'queued'
RETURNING *;

-- name: RetryTaskCompletion :one
UPDATE task_completion_outbox
SET available_at = $3,
    attempt_count = attempt_count + 1,
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error = $4,
    updated_at = now()
WHERE id = $1
  AND lease_token = $2
  AND status = 'queued'
RETURNING *;

-- name: DeadLetterTaskCompletion :one
UPDATE task_completion_outbox
SET status = 'dead_letter',
    attempt_count = attempt_count + 1,
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error = $3,
    updated_at = now()
WHERE id = $1
  AND lease_token = $2
  AND status = 'queued'
RETURNING *;

-- name: ListTerminalTaskIDsMissingCompletion :many
WITH RECURSIVE lineage AS (
    SELECT
        terminal.id AS terminal_task_id,
        terminal.id AS current_task_id,
        terminal.parent_task_id,
        terminal.context,
        COALESCE(terminal.context #>> '{completion_callback,target}', '')::text AS target_identity,
        terminal.completed_at,
        terminal.created_at
    FROM agent_task_queue terminal
    WHERE terminal.status IN ('completed', 'failed', 'cancelled')
      AND COALESCE(terminal.context #>> '{completion_callback,url}', '') <> ''
      AND jsonb_extract_path_text(
          terminal.context,
          'completion_callback',
          'target'
      ) = sqlc.arg('worker_target_identity')::text
      AND NOT EXISTS (
          SELECT 1
          FROM agent_task_queue child
          WHERE child.parent_task_id = terminal.id
      )

    UNION ALL

    SELECT
        child.terminal_task_id,
        parent.id,
        parent.parent_task_id,
        parent.context,
        COALESCE(parent.context #>> '{completion_callback,target}', '')::text,
        child.completed_at,
        child.created_at
    FROM lineage child
    JOIN agent_task_queue parent ON parent.id = child.parent_task_id
)
SELECT root.terminal_task_id
FROM lineage root
WHERE root.parent_task_id IS NULL
  AND COALESCE(root.context #>> '{completion_callback,url}', '') <> ''
  AND NOT EXISTS (
      SELECT 1
      FROM task_completion_outbox completion
      WHERE completion.root_task_id = root.current_task_id
  )
ORDER BY root.completed_at, root.created_at
LIMIT sqlc.arg('batch_limit');

-- name: GetLastTaskReplyText :one
WITH RECURSIVE lineage AS (
    SELECT task.id, task.parent_task_id
    FROM agent_task_queue task
    WHERE task.id = $1

    UNION ALL

    SELECT parent.id, parent.parent_task_id
    FROM agent_task_queue parent
    JOIN lineage child ON parent.id = child.parent_task_id
)
SELECT reply.content
FROM (
    SELECT message.content, message.created_at
    FROM task_message message
    JOIN lineage ON lineage.id = message.task_id
    WHERE message.type = 'text'
      AND COALESCE(BTRIM(message.content), '') <> ''

    UNION ALL

    SELECT task_comment.content, task_comment.created_at
    FROM comment task_comment
    JOIN lineage ON lineage.id = task_comment.source_task_id
    WHERE task_comment.author_type = 'agent'
      AND COALESCE(BTRIM(task_comment.content), '') <> ''
) AS reply
ORDER BY reply.created_at DESC
LIMIT 1;
