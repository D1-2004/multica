DELETE FROM context_scope_mcp_config WHERE scope_type = 'org';
ALTER TABLE context_scope_mcp_config
    DROP CONSTRAINT IF EXISTS context_scope_mcp_config_scope_type_check,
    ADD CONSTRAINT context_scope_mcp_config_scope_type_check CHECK (scope_type IN ('scene', 'person'));
