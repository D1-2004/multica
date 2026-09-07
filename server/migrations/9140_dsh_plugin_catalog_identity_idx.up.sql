CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS dsh_plugin_catalog_identity_idx
    ON dsh_plugin_catalog_entry (catalog, owner, name);
