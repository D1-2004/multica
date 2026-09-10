-- name: GetAgentCoordinatorContract :one
SELECT coordinator_contract FROM agent WHERE id = $1;
