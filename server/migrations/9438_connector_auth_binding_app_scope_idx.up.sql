CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS connector_auth_binding_app_scope_idx ON connector_auth_binding (app_id, scope_kind, scope_id)
