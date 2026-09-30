CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS internal_connector_catalog_slug_idx ON internal_connector (workspace_id, catalog_slug) WHERE catalog_slug <> '';
