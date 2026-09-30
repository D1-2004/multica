CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS context_config_grant_scope_idx ON context_config_grant (user_id, agent_id, scope_type, org_id, scope_key);
