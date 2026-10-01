CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS tag_config_revision_tag_rev_idx ON tag_config_revision (tag_agent_id, revision);
