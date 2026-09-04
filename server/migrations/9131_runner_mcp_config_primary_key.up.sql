ALTER TABLE runner_mcp_config
    ADD CONSTRAINT runner_mcp_config_pkey
    PRIMARY KEY USING INDEX idx_runner_mcp_config_machine;
