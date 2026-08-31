CREATE INDEX CONCURRENTLY IF NOT EXISTS hosted_site_workspace_user_idx ON hosted_site (workspace_id, owner_user_id, updated_at DESC) WHERE workspace_id IS NOT NULL AND owner_user_id IS NOT NULL;
