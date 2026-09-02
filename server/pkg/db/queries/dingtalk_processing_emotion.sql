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

-- name: GetDingTalkProcessingEmotionBySourceMessage :one
SELECT id, installation_id, source_message_id, open_conversation_id, open_msg_id, robot_code, chat_session_id, task_id, state, add_completed, attempt_count, next_attempt_at, lease_until, created_at, updated_at
FROM dingtalk_processing_emotion
WHERE source_message_id = sqlc.arg(source_message_id)
ORDER BY created_at DESC
LIMIT 1;

-- name: GetDingTalkProcessingEmotionByTask :one
-- Stream inbox binds the processing emotion to the first issue task. A
-- retry child completes under a new task id, so walk parent_task_id to
-- the row the user can still see.
WITH RECURSIVE lineage AS (
    SELECT task.id, task.parent_task_id
    FROM agent_task_queue task
    WHERE task.id = sqlc.arg(task_id)

    UNION ALL

    SELECT parent.id, parent.parent_task_id
    FROM agent_task_queue parent
    JOIN lineage child ON parent.id = child.parent_task_id
)
SELECT emotion.id, emotion.installation_id, emotion.source_message_id, emotion.open_conversation_id, emotion.open_msg_id, emotion.robot_code, emotion.chat_session_id, emotion.task_id, emotion.state, emotion.add_completed, emotion.attempt_count, emotion.next_attempt_at, emotion.lease_until, emotion.created_at, emotion.updated_at
FROM dingtalk_processing_emotion emotion
JOIN lineage ON lineage.id = emotion.task_id
ORDER BY emotion.created_at DESC
LIMIT 1;

-- name: GetLastAgentCommentForIssue :one
SELECT content
FROM comment
WHERE issue_id = sqlc.arg(issue_id)
  AND author_type = 'agent'
  AND COALESCE(BTRIM(content), '') <> ''
ORDER BY created_at DESC
LIMIT 1;
