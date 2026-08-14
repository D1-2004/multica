CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_runner_binding_connected_machine
    ON agent_runner_binding(machine_id)
    WHERE revoked_at IS NULL AND disconnected_at IS NULL;
