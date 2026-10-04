-- name: InsertA2UIInteraction :one
INSERT INTO a2ui_interaction (
    id, public_id, workspace_id, agent_id,
    sender_uid, sender_org_id, scene_id, conversation_id,
    message_id, thread_id, source_ref,
    kind, status, header, question, request, idempotency_key
) VALUES (
    sqlc.arg(id)::uuid, sqlc.arg(public_id), sqlc.arg(workspace_id)::uuid, sqlc.arg(agent_id)::uuid,
    sqlc.arg(sender_uid), sqlc.arg(sender_org_id), sqlc.arg(scene_id), sqlc.arg(conversation_id),
    sqlc.arg(message_id), sqlc.arg(thread_id), sqlc.arg(source_ref),
    sqlc.arg(kind), sqlc.arg(status), sqlc.arg(header), sqlc.arg(question),
    sqlc.arg(request)::jsonb, sqlc.arg(idempotency_key)
)
RETURNING *;

-- name: GetA2UIInteractionByPublicID :one
SELECT * FROM a2ui_interaction
WHERE public_id = sqlc.arg(public_id);

-- name: GetA2UIInteractionByIdempotency :one
SELECT * FROM a2ui_interaction
WHERE agent_id = sqlc.arg(agent_id)::uuid
  AND idempotency_key = sqlc.arg(idempotency_key)
  AND idempotency_key <> '';

-- name: GetA2UIInteractionByMessage :one
SELECT id, public_id, workspace_id, agent_id, sender_uid, sender_org_id, scene_id, conversation_id, message_id, thread_id, source_ref, kind, status, header, question, request, card_biz_id, event_id, result, operator_uid, idempotency_key, created_at, resolved_at
FROM a2ui_interaction
WHERE agent_id = sqlc.arg(agent_id)::uuid
  AND message_id = sqlc.arg(message_id)
  AND message_id <> '';

-- name: MarkA2UIInteractionSent :exec
UPDATE a2ui_interaction
SET card_biz_id = sqlc.arg(card_biz_id),
    message_id = CASE WHEN sqlc.arg(message_id) <> '' THEN sqlc.arg(message_id) ELSE message_id END,
    conversation_id = CASE WHEN sqlc.arg(conversation_id) <> '' THEN sqlc.arg(conversation_id) ELSE conversation_id END,
    status = sqlc.arg(status)
WHERE id = sqlc.arg(id)::uuid
  AND status IN ('open', 'failed');

-- name: MarkA2UIInteractionFailed :exec
UPDATE a2ui_interaction
SET status = 'failed'
WHERE id = sqlc.arg(id)::uuid
  AND card_biz_id = ''
  AND status = 'open';

-- name: ListA2UIInteractionsBySceneMessage :many
SELECT id, public_id, workspace_id, agent_id, sender_uid, sender_org_id, scene_id, conversation_id, message_id, thread_id, source_ref, kind, status, header, question, request, card_biz_id, event_id, result, operator_uid, idempotency_key, created_at, resolved_at
FROM a2ui_interaction
WHERE agent_id = sqlc.arg(agent_id)::uuid
  AND scene_id = sqlc.arg(scene_id)
  AND message_id = sqlc.arg(message_id)
  AND message_id <> ''
ORDER BY created_at DESC
LIMIT 50;

-- name: ResolveA2UIInteraction :one
UPDATE a2ui_interaction
SET status = sqlc.arg(status),
    event_id = sqlc.arg(event_id),
    operator_uid = sqlc.arg(operator_uid),
    result = sqlc.arg(result)::jsonb,
    resolved_at = now()
WHERE id = sqlc.arg(id)::uuid
  AND status = 'open'
RETURNING id;
