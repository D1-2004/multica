-- Refuse rollback while local imports exist; never delete user Agents.
ALTER TABLE agent_source
    DROP CONSTRAINT agent_source_source_type_check,
    DROP CONSTRAINT agent_source_source_mode_check,
    ADD CONSTRAINT agent_source_source_type_check CHECK (source_type IN ('github')),
    ADD CONSTRAINT agent_source_source_mode_check CHECK (
        source_type = 'github' AND (managed_source_key IS NULL OR github_installation_id IS NULL)
    );
ALTER TABLE agent_source_preview ALTER COLUMN github_installation_id SET NOT NULL;
ALTER TABLE agent_source DROP COLUMN a2a_client_mappings;
