-- name: GetResponseAction :one
SELECT * FROM response_action WHERE workspace_id = $1 AND id = $2;

-- name: ListResponseActionsByRequest :many
SELECT * FROM response_action WHERE workspace_id = $1 AND agent_id = $2 AND request_id = $3 ORDER BY created_at, id;

-- name: GetResponseRoute :one
SELECT * FROM response_route WHERE workspace_id = $1 AND callback_url = $2;
-- name: GetCoordinatorResponseCommand :one
SELECT command FROM inbound_coordinator_job
WHERE workspace_id = @workspace_id AND agent_id = @agent_id
  AND command #>> '{completionCallback,responseUrl}' = @response_callback_url::text
ORDER BY created_at DESC LIMIT 1;
