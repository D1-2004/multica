CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS connector_app_workspace_provider_client_idx ON connector_app (workspace_id, provider, client_id)
