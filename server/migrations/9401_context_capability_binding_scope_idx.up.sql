CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS context_capability_binding_scope_idx ON context_capability_binding (agent_id, scope_type, org_id, scope_key, resource_type, resource_id);
