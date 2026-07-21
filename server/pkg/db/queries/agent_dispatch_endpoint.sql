-- Agent-scoped dispatch endpoint storage. endpoint_id is a non-secret locator;
-- the delivery credential is derived in memory from the versioned keyring.

-- name: EnsureAgentDispatchEndpoint :one
INSERT INTO agent_dispatch_endpoint (
    workspace_id, agent_id, actor_user_id, endpoint_id, dispatch_url
)
SELECT
    sqlc.arg('workspace_id'),
    sqlc.arg('agent_id'),
    sqlc.arg('actor_user_id'),
    sqlc.arg('endpoint_id'),
    sqlc.arg('dispatch_url')
FROM agent a
JOIN member m
  ON m.workspace_id = a.workspace_id
 AND m.user_id = sqlc.arg('actor_user_id')
WHERE a.id = sqlc.arg('agent_id')
  AND a.workspace_id = sqlc.arg('workspace_id')
ON CONFLICT (agent_id) DO UPDATE SET
    updated_at = agent_dispatch_endpoint.updated_at
WHERE agent_dispatch_endpoint.workspace_id = EXCLUDED.workspace_id
RETURNING agent_dispatch_endpoint.*;

-- name: GetAgentDispatchEndpoint :one
SELECT ep.*
FROM agent_dispatch_endpoint ep
JOIN agent a
  ON a.id = ep.agent_id
 AND a.workspace_id = ep.workspace_id
JOIN member m
  ON m.workspace_id = ep.workspace_id
 AND m.user_id = ep.actor_user_id
WHERE ep.agent_id = sqlc.arg('agent_id');

-- name: GetAgentDispatchEndpointByEndpointID :one
SELECT ep.*
FROM agent_dispatch_endpoint ep
JOIN workspace w ON w.id = ep.workspace_id
JOIN agent a
  ON a.id = ep.agent_id
 AND a.workspace_id = ep.workspace_id
JOIN member m
  ON m.workspace_id = ep.workspace_id
 AND m.user_id = ep.actor_user_id
WHERE ep.endpoint_id = sqlc.arg('endpoint_id')::text;
