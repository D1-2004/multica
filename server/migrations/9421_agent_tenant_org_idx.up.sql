CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS agent_tenant_org_idx ON agent_tenant (agent_id, org_id);
