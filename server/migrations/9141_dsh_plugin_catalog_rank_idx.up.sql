-- Default ordering for the browse surface: most-starred first within a
-- catalog, which is how the community index itself presents entries.
CREATE INDEX CONCURRENTLY IF NOT EXISTS dsh_plugin_catalog_rank_idx
    ON dsh_plugin_catalog_entry (catalog, stars DESC);
