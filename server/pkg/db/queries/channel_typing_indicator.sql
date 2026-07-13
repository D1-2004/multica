-- name: AddChannelTypingIndicator :exec
-- Records one pending indicator. Written by the replica that ingested the
-- message; read by whichever replica ends up clearing it (see migration 168).
INSERT INTO channel_typing_indicator (
    chat_session_id, channel_type, installation_id, target
) VALUES ($1, $2, $3, $4);

-- name: TakeChannelTypingIndicators :many
-- Atomically claims every pending indicator for a session and hands them back.
-- DELETE ... RETURNING is the claim: if two replicas race to clear the same
-- run, exactly one gets the rows and only that one recalls the reactions.
DELETE FROM channel_typing_indicator
WHERE chat_session_id = $1 AND channel_type = $2
RETURNING *;
