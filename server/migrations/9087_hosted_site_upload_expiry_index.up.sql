CREATE INDEX CONCURRENTLY IF NOT EXISTS hosted_site_upload_expiry_idx ON hosted_site_upload (expires_at) WHERE used_at IS NULL;
