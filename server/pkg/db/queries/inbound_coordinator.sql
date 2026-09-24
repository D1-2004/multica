-- name: GetAgentInboundCoordinator :one
SELECT inbound_coordinator FROM agent WHERE id = $1;

-- name: UpdateAgentInboundCoordinator :exec
UPDATE agent SET inbound_coordinator = $2, inbound_coordinator_user_decision = CASE WHEN $2 THEN inbound_coordinator_user_decision ELSE false END, updated_at = now() WHERE id = $1;

-- name: ListAgentInboundCoordinatorByIDs :many
SELECT id, inbound_coordinator, inbound_coordinator_user_decision, inbound_coordinator_user_decision_names FROM agent WHERE id = ANY(sqlc.arg('ids')::uuid[]);

-- name: GetAgentTaskFinishedLoop :one
SELECT task_finished_loop_enabled FROM agent WHERE id = $1;

-- name: UpdateAgentTaskFinishedLoop :exec
UPDATE agent SET task_finished_loop_enabled = $2, updated_at = now() WHERE id = $1;

-- name: ListAgentTaskFinishedLoopByIDs :many
SELECT id, task_finished_loop_enabled FROM agent WHERE id = ANY(sqlc.arg('ids')::uuid[]);
