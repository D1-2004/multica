-- pending does not exist in the pre-binding schema. Preserve rows as inert
-- placeholders before restoring the old status constraint.
UPDATE channel_installation
SET status = 'revoked', updated_at = now()
WHERE channel_type = 'dingtalk_account'
  AND status = 'pending';

DROP INDEX IF EXISTS idx_channel_installation_dingtalk_account_endpoint;

ALTER TABLE channel_installation
    DROP CONSTRAINT IF EXISTS channel_installation_status_check;

ALTER TABLE channel_installation
    ADD CONSTRAINT channel_installation_status_check
    CHECK (status IN ('active', 'revoked'));

-- Recreate migration 183's schema so rolling the application back remains
-- possible. IF NOT EXISTS keeps an operator replay safe.
CREATE TABLE IF NOT EXISTS agent_dispatch_endpoint (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    agent_id UUID NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    actor_user_id UUID NOT NULL,
    secret_hash BYTEA NOT NULL CHECK (octet_length(secret_hash) = 32),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_dispatch_endpoint_actor_member_fk
        FOREIGN KEY (workspace_id, actor_user_id)
        REFERENCES member(workspace_id, user_id)
        ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS agent_dispatch_endpoint_agent_idx
    ON agent_dispatch_endpoint(agent_id);
