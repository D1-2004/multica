-- name: GetAgentDispatchEndpointForDelivery :one
-- The endpoint namespace a native subscription event is accepted under. Like
-- the inbound webhook lookup, delivery does not require the installer to
-- still be a workspace member.
SELECT ep.*
FROM agent_dispatch_endpoint ep
JOIN workspace w ON w.id = ep.workspace_id
JOIN agent a
  ON a.id = ep.agent_id
 AND a.workspace_id = ep.workspace_id
WHERE ep.agent_id = sqlc.arg('agent_id')
  AND ep.workspace_id = sqlc.arg('workspace_id');

-- name: IsAgentOwnDingTalkMessage :one
-- Whether a DingTalk message is one this agent sent: a managed response or a
-- sandbox send whose provider receipt named the message.
SELECT (
    sqlc.arg('message_id')::text <> ''
    AND (
        EXISTS (
            SELECT 1
            FROM response_action ra
            WHERE ra.workspace_id = sqlc.arg('workspace_id')
              AND ra.agent_id = sqlc.arg('agent_id')
              AND ra.provider_message_id = sqlc.arg('message_id')::text
        )
        OR EXISTS (
            SELECT 1
            FROM sandbox_send_receipt sr
            WHERE sr.workspace_id = sqlc.arg('workspace_id')
              AND sr.agent_id = sqlc.arg('agent_id')
              AND sr.provider_message_id = sqlc.arg('message_id')::text
        )
    )
)::boolean AS own;

-- name: GetDWSNativeAccountOwner :one
-- The agent whose native subscription owns a DingTalk account: its row names
-- the account, the agent is not archived, and the agent's current identity
-- is still that account. No row means the Router owns the account. The
-- (org_id, dws_uid) unique index allows at most one row per account.
SELECT sub.agent_id, sub.workspace_id, sub.self_open_dingtalk_id, sub.enabled_at
FROM agent_dws_native_subscription sub
JOIN agent_dingtalk_identity identity
  ON identity.agent_id = sub.agent_id
 AND identity.workspace_id = sub.workspace_id
 AND identity.dws_uid = sub.dws_uid
 AND identity.org_id = sub.org_id
JOIN agent a
  ON a.id = sub.agent_id
 AND a.workspace_id = sub.workspace_id
WHERE sub.org_id = sqlc.arg('org_id')::text
  AND sub.dws_uid = sqlc.arg('dws_uid')::text
  AND a.archived_at IS NULL;

-- name: SetDWSNativeSelfOpenDingTalkID :exec
-- Records the account's own openDingTalkId the first time one of its own
-- messages comes back; a learned value is never overwritten.
UPDATE agent_dws_native_subscription
SET self_open_dingtalk_id = sqlc.arg('self_open_dingtalk_id')::text
WHERE agent_id = sqlc.arg('agent_id')
  AND org_id = sqlc.arg('org_id')::text
  AND dws_uid = sqlc.arg('dws_uid')::text
  AND self_open_dingtalk_id = '';

-- name: IsRecentOwnReplyEcho :one
-- Whether the text is a managed reply this agent sent to the conversation in
-- the last ten minutes: DWS echoing the account's own message back.
SELECT EXISTS (
    SELECT 1
    FROM response_action ra
    WHERE ra.workspace_id = sqlc.arg('workspace_id')
      AND ra.agent_id = sqlc.arg('agent_id')
      AND ra.kind = 'message.send'
      AND ra.created_at > now() - interval '10 minutes'
      AND ra.input->>'conversation_id' = sqlc.arg('conversation_id')::text
      AND btrim(ra.input->>'text', E' \t\r\n') = sqlc.arg('content')::text
)::boolean AS echo;

-- name: HasActiveDingTalkMessageRouteForAccount :one
-- Whether an active digital-employee message binding routes this DingTalk
-- account through the Router. Router bindings name the organization by
-- corpId while identities carry the numeric org id, so the account id alone
-- decides (conservative: an equal user id in another organization also
-- counts); the per-message ownership rule is what guarantees exclusivity.
SELECT EXISTS (
    SELECT 1
    FROM channel_installation ci
    WHERE ci.channel_type = 'dingtalk_account'
      AND ci.status = 'active'
      AND ci.config->>'router_account_id' = sqlc.arg('dws_uid')::text
)::boolean AS bound;

-- name: IsDWSNativeOwnedUID :one
-- Whether native subscription owns an account with this user id in any
-- organization (a manual Router binding names the organization by corpId,
-- which the numeric org id of an identity cannot be matched against).
SELECT EXISTS (
    SELECT 1
    FROM agent_dws_native_subscription sub
    JOIN agent_dingtalk_identity identity
      ON identity.agent_id = sub.agent_id
     AND identity.workspace_id = sub.workspace_id
     AND identity.dws_uid = sub.dws_uid
     AND identity.org_id = sub.org_id
    JOIN agent a
      ON a.id = sub.agent_id
     AND a.workspace_id = sub.workspace_id
    WHERE sub.dws_uid = sqlc.arg('dws_uid')::text
      AND a.archived_at IS NULL
)::boolean AS owned;
