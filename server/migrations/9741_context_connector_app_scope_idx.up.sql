CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS context_connector_app_scope_idx ON context_connector_app (agent_id, scope_type, org_id, scope_key, provider);
