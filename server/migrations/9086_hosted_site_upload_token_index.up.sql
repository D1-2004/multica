CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS hosted_site_upload_token_hash_idx ON hosted_site_upload (token_hash);
