-- name: GetAgentVoice :one
SELECT persona, reply_tone FROM agent WHERE id = $1;

-- name: UpdateAgentVoice :exec
UPDATE agent
SET persona = sqlc.arg('persona'),
    reply_tone = sqlc.arg('reply_tone'),
    updated_at = now()
WHERE id = sqlc.arg('id');

-- name: ListAgentVoiceByIDs :many
SELECT id, persona, reply_tone FROM agent WHERE id = ANY(sqlc.arg('ids')::uuid[]);
