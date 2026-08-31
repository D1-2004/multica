CREATE INDEX CONCURRENTLY IF NOT EXISTS hosted_site_user_owner_idx ON hosted_site (owner_user_id, created_at DESC) WHERE owner_user_id IS NOT NULL;
