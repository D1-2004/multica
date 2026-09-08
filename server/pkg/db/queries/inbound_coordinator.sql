-- name: GetAgentInboundCoordinator :one
SELECT inbound_coordinator FROM agent WHERE id = $1;

-- name: UpdateAgentInboundCoordinator :exec
UPDATE agent SET inbound_coordinator = $2, updated_at = now() WHERE id = $1;

-- name: ListAgentInboundCoordinatorByIDs :many
SELECT id, inbound_coordinator FROM agent WHERE id = ANY(sqlc.arg('ids')::uuid[]);

-- name: GetAgentTaskFinishedLoop :one
SELECT task_finished_loop_enabled FROM agent WHERE id = $1;

-- name: UpdateAgentTaskFinishedLoop :exec
UPDATE agent SET task_finished_loop_enabled = $2, updated_at = now() WHERE id = $1;

-- name: ListAgentTaskFinishedLoopByIDs :many
SELECT id, task_finished_loop_enabled FROM agent WHERE id = ANY(sqlc.arg('ids')::uuid[]);
