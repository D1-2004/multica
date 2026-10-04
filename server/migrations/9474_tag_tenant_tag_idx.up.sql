CREATE INDEX CONCURRENTLY IF NOT EXISTS tag_tenant_tag_idx ON tag_tenant (tag_agent_id, created_at);
