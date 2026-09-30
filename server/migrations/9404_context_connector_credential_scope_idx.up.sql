CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS context_connector_credential_scope_idx ON context_connector_credential (agent_id, connector_id, scope_type, org_id, scope_key);
