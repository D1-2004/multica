ALTER TABLE runner_pairing_session
    ALTER COLUMN workspace_id DROP NOT NULL,
    ALTER COLUMN agent_id DROP NOT NULL;

ALTER TABLE agent_runner_binding
    ADD COLUMN IF NOT EXISTS enabled_mcp_servers JSONB NOT NULL DEFAULT '{}'::jsonb;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'agent_runner_binding_enabled_mcp_servers_object'
          AND conrelid = 'agent_runner_binding'::regclass
    ) THEN
        ALTER TABLE agent_runner_binding
            ADD CONSTRAINT agent_runner_binding_enabled_mcp_servers_object
            CHECK (jsonb_typeof(enabled_mcp_servers) = 'object');
    END IF;
END
$$;

COMMENT ON COLUMN agent_runner_binding.enabled_mcp_servers IS
    'Fail-closed allowlist of local MCP server names to their approved fingerprints';
COMMENT ON TABLE runner_pairing_session IS
    'Short-lived browser-approved OAuth device authorization for an account-owned Runner machine';
