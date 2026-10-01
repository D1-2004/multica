-- name: GetSceneEventReceipt :one
SELECT * FROM scene_event_receipt
WHERE workspace_id = @workspace_id AND agent_id = @agent_id
  AND source = @source AND source_event_id = @source_event_id;

-- name: InsertSceneEventReceipt :one
INSERT INTO scene_event_receipt (workspace_id, agent_id, principal_id, tenant_org_id,
    source, source_event_id, fingerprint, envelope, scene_id, route, state, reason, config_version)
VALUES (@workspace_id, @agent_id, @principal_id, @tenant_org_id,
    @source, @source_event_id, @fingerprint, @envelope, sqlc.narg('scene_id'),
    @route, @state, @reason, @config_version)
RETURNING *;
