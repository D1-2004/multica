-- Enterprise-level (org) custom MCP servers: scope_type 'org' with
-- scope_key = org_id. Widening only.
ALTER TABLE context_scope_mcp_config
    DROP CONSTRAINT IF EXISTS context_scope_mcp_config_scope_type_check,
    ADD CONSTRAINT context_scope_mcp_config_scope_type_check CHECK (scope_type IN ('org', 'scene', 'person'));
