CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_dws_native_subscription_account ON agent_dws_native_subscription (org_id, dws_uid);
