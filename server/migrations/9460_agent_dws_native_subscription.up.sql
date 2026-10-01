-- An agent whose DingTalk execution identity receives its own IM messages
-- through DWS native (personal event) subscriptions instead of the Agent
-- Message Router. A row means enabled. It names the account it was enabled
-- for: the account is owned by native subscription while the row's agent is
-- not archived and its current identity is still that account; otherwise
-- the Router owns it. Each account has at most one row (9461). No foreign
-- keys: the application removes the row with the identity, and listings
-- skip archived agents. self_open_dingtalk_id is the account's own
-- openDingTalkId, learned from its own delivered messages, so its outgoing
-- messages echoed back by DWS are not taken as inbound.
CREATE TABLE IF NOT EXISTS agent_dws_native_subscription (
    agent_id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL,
    enabled_by UUID NOT NULL,
    enabled_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    dws_uid TEXT NOT NULL,
    org_id TEXT NOT NULL,
    self_open_dingtalk_id TEXT NOT NULL DEFAULT ''
);
