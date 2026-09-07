CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS agent_dsh_plugin_pair_idx
    ON agent_dsh_plugin (agent_id, dsh_plugin_id);
