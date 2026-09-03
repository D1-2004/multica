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
    scene_memory_write_enabled = COALESCE(sqlc.narg('write_enabled'), scene_memory_write_enabled),
    scene_memory_recall_enabled = COALESCE(sqlc.narg('recall_enabled'), scene_memory_recall_enabled),
    scene_memory_ui_enabled = COALESCE(sqlc.narg('ui_enabled'), scene_memory_ui_enabled),
    scene_memory_bootstrap_enabled = COALESCE(sqlc.narg('bootstrap_enabled'), scene_memory_bootstrap_enabled),
    updated_at = now()
WHERE id = sqlc.arg('id');

-- name: ListAgentSceneMemoryFlagsByIDs :many
SELECT
    id,
    scene_memory_write_enabled,
    scene_memory_recall_enabled,
    scene_memory_ui_enabled,
    scene_memory_bootstrap_enabled
FROM agent
WHERE id = ANY(sqlc.arg('ids')::uuid[]);
