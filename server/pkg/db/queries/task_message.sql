-- name: CreateTaskMessage :one
INSERT INTO task_message (task_id, seq, type, tool, content, input, output)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: CreateTaskMessageEvent :one
INSERT INTO task_message (task_id, seq, type, tool, content, input, output, event)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetTaskMessageBySeq :one
SELECT * FROM task_message WHERE task_id = $1 AND seq = $2
ORDER BY created_at, id LIMIT 1;

-- name: GetTaskMessageCursor :one
SELECT COALESCE(MAX(seq), 0)::int AS seq FROM task_message WHERE task_id = $1;

-- name: ListTaskRunEvents :many
SELECT DISTINCT ON (seq) * FROM task_message
WHERE task_id = $1 AND seq > $2
AND (event IS NULL OR event->>'workspace_id' = sqlc.arg('workspace_id')::text)
AND (sqlc.arg('scene_id')::text = '' OR event->>'scene_id' = sqlc.arg('scene_id')::text)
AND (sqlc.arg('session_id')::text = '' OR event->'source'->>'session_id' = sqlc.arg('session_id')::text)
ORDER BY seq, created_at, id
LIMIT sqlc.arg('page_limit')::int;

-- name: ListTaskMessages :many
SELECT * FROM task_message
WHERE task_id = $1
ORDER BY seq ASC;

-- name: ListTaskMessagesSince :many
SELECT * FROM task_message
WHERE task_id = $1 AND seq > $2
ORDER BY seq ASC;

-- name: GetTaskMessageSummary :one
SELECT
    COUNT(*)::int AS message_count,
    COUNT(*) FILTER (WHERE type = 'tool_use')::int AS tool_call_count,
    (
        SELECT first_message.created_at
        FROM task_message AS first_message
        WHERE first_message.task_id = $1 AND first_message.type NOT IN ('status', 'log')
        ORDER BY first_message.seq ASC
        LIMIT 1
    )::timestamptz AS first_effective_reply_at
FROM task_message
WHERE task_id = $1 AND type NOT IN ('status', 'log');

-- name: ListTaskMessagesPage :many
SELECT * FROM task_message
WHERE task_id = $1 AND seq > $2
ORDER BY seq ASC
LIMIT $3;

-- name: DeleteTaskMessages :exec
DELETE FROM task_message
WHERE task_id = $1;
