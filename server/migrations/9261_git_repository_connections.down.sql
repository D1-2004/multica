DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM git_connection WHERE provider <> 'github') OR EXISTS (SELECT 1 FROM agent_source WHERE repository_url LIKE 'https://code.%') OR EXISTS (SELECT 1 FROM agent_source_preview WHERE repository LIKE 'https://code.%') OR EXISTS (SELECT 1 FROM skill WHERE config->'origin'->>'repository' LIKE 'https://code.%') THEN
        RAISE EXCEPTION 'Cannot revert GitRepo schema while Code connections or configuration history exist';
    END IF;
END $$;
ALTER TABLE agent_source DROP CONSTRAINT IF EXISTS agent_source_source_type_check;
ALTER TABLE agent_source DROP CONSTRAINT IF EXISTS agent_source_kind_check;
ALTER TABLE agent_source ADD COLUMN IF NOT EXISTS github_installation_id UUID;
ALTER TABLE agent_source_preview ADD COLUMN IF NOT EXISTS github_installation_id UUID;
UPDATE agent_source SET github_installation_id = git_connection_id, source_type = 'github' WHERE source_type = 'git';
UPDATE agent_source_preview SET github_installation_id = git_connection_id, repository = replace(repository, 'https://github.com/', '');
ALTER TABLE agent_source DROP COLUMN IF EXISTS git_connection_id;
ALTER TABLE agent_source DROP COLUMN IF EXISTS repository_url;
ALTER TABLE agent_source_preview DROP COLUMN IF EXISTS git_connection_id;
ALTER TABLE agent_source ALTER COLUMN source_type SET DEFAULT 'github';
ALTER TABLE git_connection DROP CONSTRAINT IF EXISTS git_connection_check;
ALTER TABLE git_connection DROP CONSTRAINT IF EXISTS git_connection_provider_check;
ALTER TABLE git_connection DROP COLUMN provider;
ALTER TABLE git_connection DROP COLUMN token_ciphertext;
ALTER TABLE git_connection ALTER COLUMN installation_id SET NOT NULL;
ALTER TABLE git_connection RENAME COLUMN created_by TO connected_by_id;
ALTER TABLE git_connection RENAME TO github_installation;

UPDATE agent_source_preview SET applied_source = (applied_source - 'connection_id' - 'connected') ||
    jsonb_build_object('installation_id', applied_source->'connection_id', 'github_connected', applied_source->'connected', 'source_type', 'github')
WHERE applied_source->>'source_type' = 'git';
UPDATE skill SET config = jsonb_set(config, '{origin,type}', '"github_agent_source"'::jsonb) WHERE config->'origin'->>'type' = 'git_agent_source';
ALTER TABLE agent_source ADD CONSTRAINT agent_source_source_type_check CHECK (source_type IN ('github', 'local'));
ALTER TABLE agent_source ADD CONSTRAINT agent_source_kind_check CHECK (
    (source_type = 'github' AND (managed_source_key IS NULL OR github_installation_id IS NULL))
    OR (source_type = 'local' AND managed_source_key IS NULL AND github_installation_id IS NULL AND repo_owner = '' AND repo_name = '')
);

UPDATE skill SET config = jsonb_set(config, '{origin,type}', to_jsonb(CASE WHEN config->'origin'->>'source_url' LIKE 'https://skills.sh/%' THEN 'skills_sh' ELSE 'github' END::text))
WHERE config->'origin'->>'type' = 'git';
UPDATE skill SET config = jsonb_set(config, '{origin}', (config->'origin') || jsonb_build_object(
    'owner', split_part(config->'origin'->>'repository', '/', 4),
    'repo', split_part(config->'origin'->>'repository', '/', 5)))
WHERE config->'origin'->>'type' IN ('github', 'skills_sh') AND config->'origin'->>'repository' LIKE 'https://github.com/%';
UPDATE skill SET config = jsonb_set(config, '{origin,repository}', to_jsonb(replace(config->'origin'->>'repository', 'https://github.com/', '')))
WHERE config->'origin'->>'type' = 'github_agent_source';
