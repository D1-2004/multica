DELETE FROM runner_pairing_session WHERE workspace_id IS NULL OR agent_id IS NULL;

ALTER TABLE runner_pairing_session
    ALTER COLUMN workspace_id SET NOT NULL,
    ALTER COLUMN agent_id SET NOT NULL;

ALTER TABLE agent_runner_binding
    DROP CONSTRAINT IF EXISTS agent_runner_binding_enabled_mcp_servers_object,
    DROP COLUMN IF EXISTS enabled_mcp_servers;

COMMENT ON TABLE runner_pairing_session IS
    'Short-lived browser-approved OAuth device authorization for a Runner binding';
