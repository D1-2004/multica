CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS dsh_storage_provision_identity_idx ON dsh_storage_provision (workspace_id, agent_id);
