-- Remove verified Agent identity data before pending attempts.
DROP TABLE IF EXISTS agent_dingtalk_identity;
DROP TABLE IF EXISTS agent_dingtalk_identity_attempt;

-- A rollback can restore the old schema but cannot reconstruct deleted
-- authorization archives or Agent profile selections.
CREATE TABLE IF NOT EXISTS dws_auth_profile (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    owner_id UUID REFERENCES "user"(id) ON DELETE SET NULL,
    label TEXT NOT NULL,
    corp_id TEXT NOT NULL DEFAULT '',
    corp_name TEXT NOT NULL DEFAULT '',
    user_id TEXT NOT NULL DEFAULT '',
    user_name TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    auth_archive_encrypted BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_dws_auth_profile_workspace
    ON dws_auth_profile(workspace_id, status, updated_at DESC);

CREATE INDEX IF NOT EXISTS idx_dws_auth_profile_owner
    ON dws_auth_profile(owner_id, workspace_id);
