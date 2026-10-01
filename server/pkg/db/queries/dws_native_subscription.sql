-- name: GetAgentDWSNativeSubscription :one
SELECT agent_id, workspace_id, enabled_by, enabled_at, dws_uid, org_id, self_open_dingtalk_id
FROM agent_dws_native_subscription
WHERE workspace_id = sqlc.arg('workspace_id')
  AND agent_id = sqlc.arg('agent_id');

-- name: EnableAgentDWSNativeSubscription :one
-- Enabling names the account; re-enabling after a rebind moves the row to
-- the new account and forgets the old account's learned openDingTalkId. The
-- (org_id, dws_uid) unique index rejects an account another agent holds.
INSERT INTO agent_dws_native_subscription (agent_id, workspace_id, enabled_by, dws_uid, org_id)
VALUES (sqlc.arg('agent_id'), sqlc.arg('workspace_id'), sqlc.arg('enabled_by'), sqlc.arg('dws_uid'), sqlc.arg('org_id'))
ON CONFLICT (agent_id) DO UPDATE
SET dws_uid = EXCLUDED.dws_uid,
    org_id = EXCLUDED.org_id,
    self_open_dingtalk_id = CASE
        WHEN agent_dws_native_subscription.dws_uid = EXCLUDED.dws_uid
         AND agent_dws_native_subscription.org_id = EXCLUDED.org_id
        THEN agent_dws_native_subscription.self_open_dingtalk_id
        ELSE ''
    END
WHERE agent_dws_native_subscription.workspace_id = EXCLUDED.workspace_id
RETURNING agent_id, workspace_id, enabled_by, enabled_at, dws_uid, org_id, self_open_dingtalk_id;

-- name: DisableAgentDWSNativeSubscription :exec
DELETE FROM agent_dws_native_subscription
WHERE workspace_id = sqlc.arg('workspace_id')
  AND agent_id = sqlc.arg('agent_id');

-- name: ListWorkspaceDWSNativeSubscriptions :many
SELECT agent_id, workspace_id, enabled_by, enabled_at, dws_uid, org_id, self_open_dingtalk_id
FROM agent_dws_native_subscription
WHERE workspace_id = sqlc.arg('workspace_id')
ORDER BY enabled_at ASC, agent_id ASC;

-- name: ListActiveDWSNativeSubscriptions :many
-- Every account native subscription owns, which the native event source
-- streams: a row whose agent is not archived and whose agent's current
-- identity is still the row's account. Ownership alone decides; a Router
-- message binding for the same account does not take it back (the Router
-- path drops deliveries for owned accounts).
SELECT sub.agent_id, sub.workspace_id, sub.dws_uid, sub.org_id
FROM agent_dws_native_subscription sub
JOIN agent_dingtalk_identity identity
  ON identity.agent_id = sub.agent_id
 AND identity.workspace_id = sub.workspace_id
 AND identity.dws_uid = sub.dws_uid
 AND identity.org_id = sub.org_id
JOIN agent a
  ON a.id = sub.agent_id
 AND a.workspace_id = sub.workspace_id
WHERE a.archived_at IS NULL
ORDER BY sub.enabled_at ASC, sub.agent_id ASC;

-- name: DeleteStaleDWSNativeSubscriptionsForAccount :exec
-- Rows for an account that no longer own it (the agent is archived or its
-- identity moved to another account) must not keep the account's unique
-- slot; enabling another agent clears them first.
DELETE FROM agent_dws_native_subscription sub
WHERE sub.org_id = sqlc.arg('org_id')::text
  AND sub.dws_uid = sqlc.arg('dws_uid')::text
  AND sub.agent_id <> sqlc.arg('except_agent_id')
  AND NOT EXISTS (
      SELECT 1
      FROM agent_dingtalk_identity identity
      JOIN agent a
        ON a.id = identity.agent_id
       AND a.workspace_id = identity.workspace_id
      WHERE identity.agent_id = sub.agent_id
        AND identity.workspace_id = sub.workspace_id
        AND identity.dws_uid = sub.dws_uid
        AND identity.org_id = sub.org_id
        AND a.archived_at IS NULL
  );

-- name: ClearDWSNativeSubscriptionOnAccountChange :exec
-- Rebinding an agent's identity to another account ends that agent's native
-- subscription; it is enabled again for the new account explicitly.
DELETE FROM agent_dws_native_subscription
WHERE agent_id = sqlc.arg('agent_id')
  AND (dws_uid <> sqlc.arg('dws_uid')::text OR org_id <> sqlc.arg('org_id')::text);
