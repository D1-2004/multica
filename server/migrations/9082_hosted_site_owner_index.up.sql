CREATE INDEX CONCURRENTLY IF NOT EXISTS hosted_site_owner_idx ON hosted_site (workspace_id, owner_agent_id, created_at DESC);
