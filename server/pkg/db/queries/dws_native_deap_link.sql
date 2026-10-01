-- name: GetDWSNativeDEAPLink :one
-- The agent's DEAP link, only while it names the agent's current account.
SELECT agent_id, workspace_id, dws_uid, org_id, deap_agent_uuid, supervisor_uid, updated_by, updated_at
FROM agent_dws_native_deap_link
WHERE agent_id = sqlc.arg('agent_id')
  AND dws_uid = sqlc.arg('dws_uid')::text
  AND org_id = sqlc.arg('org_id')::text;

-- name: GetWorkspaceDWSNativeDEAPLink :one
SELECT agent_id, workspace_id, dws_uid, org_id, deap_agent_uuid, supervisor_uid, updated_by, updated_at
FROM agent_dws_native_deap_link
WHERE workspace_id = sqlc.arg('workspace_id')
  AND agent_id = sqlc.arg('agent_id');

-- name: UpsertDWSNativeDEAPLink :one
INSERT INTO agent_dws_native_deap_link (agent_id, workspace_id, dws_uid, org_id, deap_agent_uuid, supervisor_uid, updated_by)
VALUES (sqlc.arg('agent_id'), sqlc.arg('workspace_id'), sqlc.arg('dws_uid'), sqlc.arg('org_id'),
        sqlc.arg('deap_agent_uuid'), sqlc.arg('supervisor_uid'), sqlc.arg('updated_by'))
ON CONFLICT (agent_id) DO UPDATE
SET dws_uid = EXCLUDED.dws_uid,
    org_id = EXCLUDED.org_id,
    deap_agent_uuid = EXCLUDED.deap_agent_uuid,
    supervisor_uid = EXCLUDED.supervisor_uid,
    updated_by = EXCLUDED.updated_by,
    updated_at = now()
WHERE agent_dws_native_deap_link.workspace_id = EXCLUDED.workspace_id
RETURNING agent_id, workspace_id, dws_uid, org_id, deap_agent_uuid, supervisor_uid, updated_by, updated_at;

-- name: DeleteDWSNativeDEAPLink :exec
DELETE FROM agent_dws_native_deap_link
WHERE workspace_id = sqlc.arg('workspace_id')
  AND agent_id = sqlc.arg('agent_id');

-- name: ListDWSNativeDEAPLinks :many
-- Every DEAP link (a handful): the native event source versions its
-- identities' credentials by them.
SELECT agent_id, workspace_id, dws_uid, org_id, deap_agent_uuid, supervisor_uid, updated_by, updated_at
FROM agent_dws_native_deap_link;
