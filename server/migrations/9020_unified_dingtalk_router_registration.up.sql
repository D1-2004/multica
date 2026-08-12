-- One stable Multica dispatch endpoint belongs to an Agent, not to one
-- DingTalk account or robot source. Router stores one delivery target per
-- agent, so every source attached to that agent must reuse this URL.
CREATE TABLE IF NOT EXISTS agent_dispatch_endpoint (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    actor_user_id UUID NOT NULL,
    endpoint_id TEXT NOT NULL,
    dispatch_url TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (agent_id),
    UNIQUE (endpoint_id),
    CONSTRAINT agent_dispatch_endpoint_actor_member_fk
        FOREIGN KEY (workspace_id, actor_user_id)
        REFERENCES member(workspace_id, user_id)
        ON DELETE CASCADE
);

-- Preserve the endpoint already registered for each digital-employee source.
-- Orphaned rows are intentionally skipped: dispatch must fail closed when the
-- agent or actor membership no longer exists.
INSERT INTO agent_dispatch_endpoint (
    workspace_id, agent_id, actor_user_id, endpoint_id, dispatch_url
)
SELECT
    ci.workspace_id,
    ci.agent_id,
    ci.installer_user_id,
    ci.config ->> 'dispatch_endpoint_id',
    ci.config ->> 'dispatch_url'
FROM channel_installation ci
JOIN agent a
  ON a.id = ci.agent_id
 AND a.workspace_id = ci.workspace_id
JOIN member m
  ON m.workspace_id = ci.workspace_id
 AND m.user_id = ci.installer_user_id
WHERE ci.channel_type = 'dingtalk_account'
  AND NULLIF(ci.config ->> 'dispatch_endpoint_id', '') IS NOT NULL
  AND NULLIF(ci.config ->> 'dispatch_url', '') IS NOT NULL
ON CONFLICT (agent_id) DO NOTHING;
