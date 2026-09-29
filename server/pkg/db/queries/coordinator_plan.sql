-- name: SaveCoordinatorWindowPlan :execrows
UPDATE inbound_coordinator_job
SET command = jsonb_set(command, '{_coordinator_plan}', sqlc.arg(plan)::jsonb),
    updated_at = now()
WHERE id = @id AND status = 'running' AND lease_token = @lease_token AND lease_expires_at > now();

-- name: SaveCoordinatorFinishSchema :execrows
UPDATE inbound_coordinator_job
SET command = jsonb_set(command, '{_finish_schema_experiment}', sqlc.arg(record)::jsonb),
    updated_at = now()
WHERE id = @id AND status = 'running' AND lease_token = @lease_token AND lease_expires_at > now();
