CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS dsh_profile_revision_identity ON dsh_profile_revision (workspace_id, agent_id, revision);
