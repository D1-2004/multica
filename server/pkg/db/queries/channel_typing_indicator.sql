-- name: AddChannelTypingIndicator :exec
-- Records one pending indicator. Written by the replica that ingested the
-- message; read by whichever replica ends up clearing it (see migration 182).
INSERT INTO channel_typing_indicator (
    chat_session_id, channel_type, installation_id, target
) VALUES ($1, $2, $3, $4);

-- name: TakeChannelTypingIndicatorsByTask :many
-- Atomically claims the pending indicators owned by one task. The task id is
-- stored in the platform-shaped JSON target so the shared table remains
-- schema-neutral while concurrent turns in one chat session stay isolated.
DELETE FROM channel_typing_indicator
WHERE chat_session_id = sqlc.arg(chat_session_id)
  AND channel_type = sqlc.arg(channel_type)
  AND target->>'task_id' = sqlc.arg(task_id)::text
RETURNING *;

-- name: TakeChannelTypingIndicators :many
-- Atomically claims every pending indicator for a session and hands them back.
-- DELETE ... RETURNING is the claim: if two replicas race to clear the same
-- run, exactly one gets the rows and only that one recalls the reactions.
DELETE FROM channel_typing_indicator
WHERE chat_session_id = $1 AND channel_type = $2
RETURNING *;
