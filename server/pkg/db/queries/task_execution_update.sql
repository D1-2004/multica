-- =============================
-- Task execution update outbox
-- =============================

-- name: EnqueueTaskExecutionUpdate :one
INSERT INTO task_execution_update_outbox AS existing (
    root_task_id,
    target_task_id,
    issue_id,
    issue_identifier,
    callback_url,
    target_identity,
    request_id,
    agent_id,
    target_agent_id,
    update_type,
    status,
    result_message_frozen
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
    CASE WHEN sqlc.arg('result_message_frozen') THEN 'queued' ELSE 'waiting_result' END,
    sqlc.arg('result_message_frozen')
)
ON CONFLICT (root_task_id) DO UPDATE
SET updated_at = existing.updated_at
WHERE existing.target_task_id = EXCLUDED.target_task_id
  AND existing.issue_id = EXCLUDED.issue_id
  AND existing.issue_identifier = EXCLUDED.issue_identifier
  AND existing.callback_url = EXCLUDED.callback_url
  AND existing.target_identity = EXCLUDED.target_identity
  AND existing.request_id = EXCLUDED.request_id
  AND existing.agent_id = EXCLUDED.agent_id
  AND existing.target_agent_id = EXCLUDED.target_agent_id
  AND existing.update_type = EXCLUDED.update_type
RETURNING *;

-- name: EnqueueFrozenTaskExecutionUpdate :one
INSERT INTO task_execution_update_outbox AS existing (
    root_task_id,
    target_task_id,
    issue_id,
    issue_identifier,
    callback_url,
    target_identity,
    request_id,
    agent_id,
    target_agent_id,
    update_type,
    status,
    result_message_frozen,
    result_message
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'queued', TRUE, $11
)
ON CONFLICT (root_task_id) DO UPDATE
SET updated_at = existing.updated_at
WHERE existing.target_task_id = EXCLUDED.target_task_id
  AND existing.issue_id = EXCLUDED.issue_id
  AND existing.issue_identifier = EXCLUDED.issue_identifier
  AND existing.callback_url = EXCLUDED.callback_url
  AND existing.target_identity = EXCLUDED.target_identity
  AND existing.request_id = EXCLUDED.request_id
  AND existing.agent_id = EXCLUDED.agent_id
  AND existing.target_agent_id = EXCLUDED.target_agent_id
  AND existing.update_type = EXCLUDED.update_type
  AND existing.result_message_frozen = TRUE
  AND existing.result_message IS NOT DISTINCT FROM EXCLUDED.result_message
RETURNING *;

-- name: FreezeTaskExecutionUpdateResultMessage :one
WITH RECURSIVE lineage AS (
    SELECT task.id, task.parent_task_id
    FROM agent_task_queue task
    WHERE task.id = sqlc.arg('terminal_task_id')

    UNION ALL

    SELECT parent.id, parent.parent_task_id
    FROM agent_task_queue parent
    JOIN lineage child ON parent.id = child.parent_task_id
)
UPDATE task_execution_update_outbox AS execution_update
SET result_message = sqlc.narg('result_message'),
    result_message_frozen = TRUE,
    status = 'queued',
    available_at = now(),
    updated_at = now()
WHERE execution_update.root_task_id IN (SELECT id FROM lineage)
  AND execution_update.status = 'waiting_result'
  AND execution_update.result_message_frozen = FALSE
RETURNING execution_update.*;

-- name: ClaimTaskExecutionUpdate :one
WITH candidate AS (
    SELECT queued.id
    FROM task_execution_update_outbox queued
    WHERE queued.status = 'queued'
      AND queued.target_identity = $1
      AND queued.result_message_frozen
      AND queued.available_at <= now()
      AND (queued.lease_expires_at IS NULL OR queued.lease_expires_at <= now())
    ORDER BY queued.available_at ASC, queued.created_at ASC
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
UPDATE task_execution_update_outbox AS execution_update
SET lease_token = gen_random_uuid(),
    lease_expires_at = now() + interval '2 minutes',
    attempt_count = execution_update.attempt_count + 1,
    updated_at = now()
FROM candidate
WHERE execution_update.id = candidate.id
RETURNING execution_update.*;

-- name: CompleteTaskExecutionUpdate :one
UPDATE task_execution_update_outbox
SET status = 'delivered',
    delivered_at = now(),
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error = NULL,
    updated_at = now()
WHERE id = $1
  AND lease_token = $2
  AND status = 'queued'
RETURNING *;

-- name: RetryTaskExecutionUpdate :one
UPDATE task_execution_update_outbox
SET available_at = $3,
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error = $4,
    updated_at = now()
WHERE id = $1
  AND lease_token = $2
  AND status = 'queued'
RETURNING *;

-- name: DeadLetterTaskExecutionUpdate :one
UPDATE task_execution_update_outbox
SET status = 'dead_letter',
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error = $3,
    updated_at = now()
WHERE id = $1
  AND lease_token = $2
  AND status = 'queued'
RETURNING *;

-- name: SaveTaskExecutionUpdateDWSDelivery :one
UPDATE task_execution_update_outbox
SET dws_delivery = $3,
    updated_at = now()
WHERE id = $1 AND lease_token = $2 AND status = 'queued'
RETURNING *;
