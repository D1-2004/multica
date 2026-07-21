-- name: BeginDingTalkProcessingEmotion :one
INSERT INTO dingtalk_processing_emotion (
    installation_id,
    source_message_id,
    open_conversation_id,
    open_msg_id,
    robot_code,
    state,
    next_attempt_at
) VALUES ($1, $2, $3, $4, $5, 'adding', now() + interval '10 seconds')
ON CONFLICT (installation_id, source_message_id) DO NOTHING
RETURNING *;

-- name: BindDingTalkProcessingEmotionTask :one
UPDATE dingtalk_processing_emotion
SET chat_session_id = sqlc.arg(chat_session_id),
    task_id = sqlc.arg(task_id),
    updated_at = now()
WHERE installation_id = sqlc.arg(installation_id)
  AND source_message_id = sqlc.arg(source_message_id)
RETURNING *;

-- name: MarkDingTalkProcessingEmotionAdded :one
UPDATE dingtalk_processing_emotion
SET state = CASE WHEN state = 'settled' THEN 'settled' ELSE 'active' END,
    add_completed = true,
    lease_until = NULL,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SettleDingTalkProcessingEmotionTask :many
UPDATE dingtalk_processing_emotion
SET state = 'settled',
    next_attempt_at = CASE WHEN add_completed THEN now() ELSE now() + interval '4 seconds' END,
    lease_until = NULL,
    updated_at = now()
WHERE chat_session_id = sqlc.arg(chat_session_id)
  AND task_id = sqlc.arg(task_id)
RETURNING *;

-- name: SettleDingTalkProcessingEmotionSource :many
UPDATE dingtalk_processing_emotion
SET state = 'settled',
    next_attempt_at = CASE WHEN add_completed THEN now() ELSE now() + interval '4 seconds' END,
    lease_until = NULL,
    updated_at = now()
WHERE installation_id = sqlc.arg(installation_id)
  AND source_message_id = sqlc.arg(source_message_id)
RETURNING *;

-- name: MarkOrphanedDingTalkProcessingEmotionsSettled :many
WITH due AS (
    SELECT id
    FROM dingtalk_processing_emotion
    WHERE chat_session_id IS NULL
      AND state IN ('adding', 'active')
      AND created_at < now() - interval '2 minutes'
    ORDER BY created_at
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(batch_size)
)
UPDATE dingtalk_processing_emotion AS emotion
SET state = 'settled',
    next_attempt_at = CASE WHEN emotion.add_completed THEN now() ELSE now() + interval '4 seconds' END,
    lease_until = NULL,
    updated_at = now()
FROM due
WHERE emotion.id = due.id
RETURNING emotion.*;

-- name: ClaimDueDingTalkProcessingEmotions :many
WITH due AS (
    SELECT id
    FROM dingtalk_processing_emotion
    WHERE state IN ('adding', 'settled')
      AND next_attempt_at <= now()
      AND (lease_until IS NULL OR lease_until < now())
    ORDER BY next_attempt_at, created_at
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(batch_size)
)
UPDATE dingtalk_processing_emotion AS emotion
SET lease_until = now() + interval '30 seconds',
    updated_at = now()
FROM due
WHERE emotion.id = due.id
RETURNING emotion.*;

-- name: RetryDingTalkProcessingEmotion :exec
UPDATE dingtalk_processing_emotion
SET attempt_count = attempt_count + 1,
    next_attempt_at = sqlc.arg(next_attempt_at),
    lease_until = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: DeleteDingTalkProcessingEmotion :exec
DELETE FROM dingtalk_processing_emotion WHERE id = $1;
