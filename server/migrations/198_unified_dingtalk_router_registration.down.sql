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

DROP TABLE IF EXISTS agent_dispatch_endpoint;
