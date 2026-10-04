CREATE INDEX CONCURRENTLY IF NOT EXISTS connector_app_workspace_provider_idx ON connector_app (workspace_id, provider, created_at, id)
