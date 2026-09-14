-- NULL inherits the workspace default; an object is private to this employee.
CREATE SEQUENCE IF NOT EXISTS agent_dsh_plugin_config_revision_seq;

ALTER TABLE agent_dsh_plugin
    ADD COLUMN IF NOT EXISTS config_override JSONB
        CHECK (jsonb_typeof(config_override) = 'object' AND octet_length(config_override::text) <= 65536),
    ADD COLUMN IF NOT EXISTS config_revision BIGINT NOT NULL DEFAULT nextval('agent_dsh_plugin_config_revision_seq');
