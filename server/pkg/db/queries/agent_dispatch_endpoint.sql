-- name: GetActiveAgentDispatchEndpoint :one
-- Resolve the public endpoint id and delivery credential together. Joining the
-- member and agent tables makes stale or cross-workspace endpoint rows inert.
SELECT ade.*
FROM agent_dispatch_endpoint ade
JOIN member m
  ON m.workspace_id = ade.workspace_id
 AND m.user_id = ade.actor_user_id
JOIN agent a
  ON a.id = ade.agent_id
 AND a.workspace_id = ade.workspace_id
WHERE ade.id = sqlc.arg('id')
  AND ade.secret_hash = sqlc.arg('secret_hash')
  AND ade.status = 'active';
