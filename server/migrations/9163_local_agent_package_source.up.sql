-- Local uploads use the same immutable preview and source-skill ledger as Git.
ALTER TABLE agent_source_preview ALTER COLUMN github_installation_id DROP NOT NULL;

-- Stable manifest client keys map only to clients owned by this source.
ALTER TABLE agent_source ADD COLUMN a2a_client_mappings JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE agent_source
    DROP CONSTRAINT agent_source_source_type_check,
    DROP CONSTRAINT agent_source_source_mode_check,
    ADD CONSTRAINT agent_source_source_type_check CHECK (source_type IN ('github', 'local')),
    ADD CONSTRAINT agent_source_source_mode_check CHECK (
        (source_type = 'github' AND (managed_source_key IS NULL OR github_installation_id IS NULL))
        OR (source_type = 'local' AND managed_source_key IS NULL AND github_installation_id IS NULL
            AND repo_owner = '' AND repo_name = '' AND ref = '')
    );
