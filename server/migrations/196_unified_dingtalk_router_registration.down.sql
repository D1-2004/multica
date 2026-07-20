-- Restore account-local endpoint fields before removing the Agent-level table
-- so an older Multica build can continue resolving digital-employee dispatch.
UPDATE channel_installation ci
SET config = jsonb_set(
        jsonb_set(ci.config, '{dispatch_endpoint_id}', to_jsonb(ep.endpoint_id), true),
        '{dispatch_url}',
        to_jsonb(ep.dispatch_url),
        true
    ),
    updated_at = now()
FROM agent_dispatch_endpoint ep
WHERE ci.channel_type = 'dingtalk_account'
  AND ci.agent_id = ep.agent_id
  AND ci.workspace_id = ep.workspace_id;

-- The old binary knows only legacy Stream ingress. Pending Router work that
-- never left legacy Stream becomes active again. A callback-pending or fully
-- switched robot is revoked instead: the down migration cannot safely undo
-- DingTalk's external callback configuration, so fail closed rather than let
-- the old Stream engine create a second ingress.
UPDATE channel_installation
SET config = config
        - 'router_source_id'
        - 'router_agent_id'
        - 'router_registration_status'
        - 'ingress_cutover_state',
    status = CASE
        WHEN config ->> 'ingress_cutover_state' IN ('callback_pending', 'gateway_callback')
            THEN 'revoked'
        WHEN status = 'pending' THEN 'active'
        ELSE status
    END,
    updated_at = now()
WHERE channel_type = 'dingtalk';

DROP TABLE IF EXISTS agent_dispatch_endpoint;
