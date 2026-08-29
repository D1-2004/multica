CREATE INDEX CONCURRENTLY IF NOT EXISTS hosted_site_revision_site_idx ON hosted_site_revision (site_id, created_at DESC);
