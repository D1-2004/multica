CREATE TABLE IF NOT EXISTS runner_mcp_config (
    machine_id UUID NOT NULL,
    config BYTEA NOT NULL,
    revision TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT runner_mcp_config_document_size CHECK (octet_length(config) <= 1048576)
);

COMMENT ON TABLE runner_mcp_config IS
    'Exact unmodified bytes last reported from ~/.multica/runner/mcp.json; machine lifecycle is enforced by application code';
