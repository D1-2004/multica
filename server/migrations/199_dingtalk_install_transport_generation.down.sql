DROP INDEX IF EXISTS idx_dingtalk_install_session_agent_generation;

ALTER TABLE dingtalk_install_session
    DROP COLUMN IF EXISTS generation,
    DROP COLUMN IF EXISTS allow_unbound,
    DROP COLUMN IF EXISTS transport_mode;
