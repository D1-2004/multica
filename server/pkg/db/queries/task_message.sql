-- name: CreateTaskMessage :one
INSERT INTO task_message (task_id, seq, type, tool, content, input, output)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

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
    COUNT(*) FILTER (WHERE type = 'tool_use')::int AS tool_call_count
FROM task_message
WHERE task_id = $1;

-- name: ListTaskMessagesPage :many
SELECT * FROM task_message
WHERE task_id = $1 AND seq > $2
ORDER BY seq ASC
LIMIT $3;

-- name: DeleteTaskMessages :exec
DELETE FROM task_message
WHERE task_id = $1;
