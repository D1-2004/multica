CREATE INDEX CONCURRENTLY IF NOT EXISTS context_config_grant_agent_scope_idx ON context_config_grant (agent_id, scope_type, org_id, scope_key);
