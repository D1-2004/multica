-- An Agent's DingTalk account binding records two independent outcomes:
-- message routing remains in channel_installation, while DWS identity is
-- stored here after OrgEmpService validates the scanned staffId and orgId.
CREATE TABLE agent_dingtalk_identity_attempt (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    initiator_user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    callback_token_hash TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    completed_open_id TEXT,
    completed_org_id TEXT,
    completed_corp_id TEXT,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((used_at IS NULL) = (completed_open_id IS NULL)),
    CHECK ((used_at IS NULL) = (completed_org_id IS NULL)),
    CHECK ((used_at IS NULL) = (completed_corp_id IS NULL))
);

CREATE INDEX idx_agent_dingtalk_identity_attempt_agent
    ON agent_dingtalk_identity_attempt(workspace_id, agent_id, created_at DESC);

CREATE INDEX idx_agent_dingtalk_identity_attempt_expiry
    ON agent_dingtalk_identity_attempt(expires_at)
    WHERE used_at IS NULL;

CREATE TABLE agent_dingtalk_identity (
    agent_id UUID PRIMARY KEY REFERENCES agent(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    account_open_id TEXT NOT NULL,
    account_corp_id TEXT NOT NULL,
    dws_uid TEXT NOT NULL,
    org_id TEXT NOT NULL,
    account_display_name TEXT NOT NULL DEFAULT '',
    account_avatar_url TEXT NOT NULL DEFAULT '',
    bound_by UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
    bound_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, agent_id)
);

CREATE INDEX idx_agent_dingtalk_identity_workspace
    ON agent_dingtalk_identity(workspace_id, bound_at DESC);

-- ContextToken is now the only DWS authorization path. Remove both historical
-- runtime_config locations before dropping the encrypted profile store.
UPDATE agent
SET runtime_config = (runtime_config - 'dws_profile_id') #- '{fc_e2b,dws_profile_id}'
WHERE runtime_config ? 'dws_profile_id'
   OR runtime_config #> '{fc_e2b,dws_profile_id}' IS NOT NULL;

DROP TABLE IF EXISTS dws_auth_profile;
