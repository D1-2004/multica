-- name: GetAgentPackageBindingState :one
SELECT COALESCE((SELECT package_binding_state FROM agent_source WHERE agent_id = sqlc.arg(agent_id) AND workspace_id = sqlc.arg(workspace_id)), '{}'::jsonb)::jsonb;

-- name: UpdateAgentPackageBindingState :execrows
UPDATE agent_source SET package_binding_state = sqlc.arg(state), updated_at = now()
WHERE agent_id = sqlc.arg(agent_id) AND workspace_id = sqlc.arg(workspace_id);

-- name: GetAgentPackageEventTriggerEnabled :one
SELECT COALESCE((SELECT enabled FROM agent_event_trigger WHERE agent_id = sqlc.arg(agent_id) AND workspace_id = sqlc.arg(workspace_id)), false)::boolean;
