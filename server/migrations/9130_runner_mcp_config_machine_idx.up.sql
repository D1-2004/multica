CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_runner_mcp_config_machine
    ON runner_mcp_config(machine_id);
