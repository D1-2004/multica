-- name: GetAgentSceneMemoryFlags :one
SELECT
    scene_memory_write_enabled,
    scene_memory_recall_enabled,
    scene_memory_ui_enabled,
    scene_memory_bootstrap_enabled
FROM agent
WHERE id = $1;

-- name: UpdateAgentSceneMemoryFlags :exec
UPDATE agent SET
    scene_memory_write_enabled = $2,
    scene_memory_recall_enabled = $3,
    scene_memory_ui_enabled = $4,
    scene_memory_bootstrap_enabled = $5,
    updated_at = now()
WHERE id = $1;

-- name: ListAgentSceneMemoryFlagsByIDs :many
SELECT
    id,
    scene_memory_write_enabled,
    scene_memory_recall_enabled,
    scene_memory_ui_enabled,
    scene_memory_bootstrap_enabled
FROM agent
WHERE id = ANY(sqlc.arg('ids')::uuid[]);
