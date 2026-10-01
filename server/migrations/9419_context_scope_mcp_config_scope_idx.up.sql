CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS context_scope_mcp_config_scope_idx ON context_scope_mcp_config (agent_id, scope_type, org_id, scope_key);
