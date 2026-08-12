ALTER TABLE dingtalk_install_session
    ADD COLUMN IF NOT EXISTS transport_mode TEXT NOT NULL DEFAULT 'STREAM'
        CHECK (transport_mode IN ('STREAM', 'HTTP_CALLBACK')),
    ADD COLUMN IF NOT EXISTS allow_unbound BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS generation BIGINT NOT NULL DEFAULT 1
        CHECK (generation > 0);

CREATE INDEX IF NOT EXISTS idx_dingtalk_install_session_agent_generation
    ON dingtalk_install_session (workspace_id, agent_id, generation DESC);
