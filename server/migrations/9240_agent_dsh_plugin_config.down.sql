ALTER TABLE agent_dsh_plugin
    DROP COLUMN IF EXISTS config_override,
    DROP COLUMN IF EXISTS config_revision;

DROP SEQUENCE IF EXISTS agent_dsh_plugin_config_revision_seq;
