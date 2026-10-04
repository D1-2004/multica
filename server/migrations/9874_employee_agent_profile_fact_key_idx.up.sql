CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_agent_profile_fact_key_idx ON employee_agent_profile_fact (workspace_id, agent_id, tenant_org_id, fact_key);
