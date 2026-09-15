CREATE TABLE IF NOT EXISTS git_connection (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    provider TEXT NOT NULL CHECK (provider IN ('github', 'alibaba_code')),
    account_login TEXT NOT NULL,
    account_type TEXT NOT NULL DEFAULT 'User',
    account_avatar_url TEXT,
    installation_id BIGINT,
    token_ciphertext BYTEA,
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((provider = 'github' AND installation_id IS NOT NULL AND installation_id > 0 AND token_ciphertext IS NULL)
        OR (provider = 'alibaba_code' AND installation_id IS NULL AND token_ciphertext IS NOT NULL))
);

DO $$ BEGIN
    IF to_regclass('public.github_installation') IS NOT NULL THEN
        INSERT INTO git_connection (id, workspace_id, provider, account_login, account_type, account_avatar_url, installation_id, created_by, created_at, updated_at)
        SELECT gi.id, gi.workspace_id, 'github', gi.account_login, gi.account_type, gi.account_avatar_url, gi.installation_id, gi.connected_by_id, gi.created_at, gi.updated_at
        FROM github_installation gi
        WHERE NOT EXISTS (SELECT 1 FROM git_connection gc WHERE gc.id = gi.id);
    END IF;
END $$;

ALTER TABLE agent_source ADD COLUMN IF NOT EXISTS git_connection_id UUID;
ALTER TABLE agent_source ADD COLUMN IF NOT EXISTS repository_url TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_source_preview ADD COLUMN IF NOT EXISTS git_connection_id UUID;

DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'agent_source' AND column_name = 'github_installation_id') THEN
        UPDATE agent_source SET git_connection_id = github_installation_id;
    END IF;
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'agent_source_preview' AND column_name = 'github_installation_id') THEN
        UPDATE agent_source_preview SET git_connection_id = github_installation_id;
    END IF;
END $$;

ALTER TABLE agent_source DROP COLUMN IF EXISTS github_installation_id;
ALTER TABLE agent_source_preview DROP COLUMN IF EXISTS github_installation_id;
ALTER TABLE agent_source DROP CONSTRAINT IF EXISTS agent_source_source_type_check;
ALTER TABLE agent_source DROP CONSTRAINT IF EXISTS agent_source_kind_check;
ALTER TABLE agent_source ALTER COLUMN source_type SET DEFAULT 'git';
UPDATE agent_source SET repository_url = 'https://github.com/' || repo_owner || '/' || repo_name, source_type = 'git'
WHERE source_type = 'github';
UPDATE agent_source_preview SET repository = 'https://github.com/' || repository
WHERE git_connection_id IS NOT NULL AND repository NOT LIKE 'https://%';
UPDATE agent_source_preview SET applied_source =
    (applied_source - 'installation_id' - 'github_connected') ||
    jsonb_build_object('connection_id', applied_source->'installation_id',
        'connected', applied_source->'github_connected', 'source_type', 'git')
WHERE applied_source->>'source_type' = 'github';

UPDATE skill SET config = jsonb_set(config, '{origin,type}', '"git_agent_source"'::jsonb)
WHERE config->'origin'->>'type' = 'github_agent_source';

ALTER TABLE agent_source DROP CONSTRAINT IF EXISTS agent_source_kind_check;
ALTER TABLE agent_source ADD CONSTRAINT agent_source_source_type_check CHECK (source_type IN ('git', 'local'));
ALTER TABLE agent_source ADD CONSTRAINT agent_source_kind_check CHECK (
    (source_type = 'git' AND (managed_source_key IS NULL OR git_connection_id IS NULL))
    OR (source_type = 'local' AND managed_source_key IS NULL AND git_connection_id IS NULL AND repository_url = '')
);

DROP TABLE IF EXISTS github_installation;

UPDATE skill SET config = jsonb_set(config, '{origin}', (config->'origin') ||
    jsonb_build_object('type','git','repository','https://github.com/' || (config->'origin'->>'owner') || '/' || (config->'origin'->>'repo')))
WHERE config->'origin'->>'type' IN ('github','skills_sh');
UPDATE skill SET config = jsonb_set(config, '{origin,repository}', to_jsonb('https://github.com/' || (config->'origin'->>'repository')))
WHERE config->'origin'->>'type' = 'git_agent_source' AND config->'origin'->>'repository' NOT LIKE 'https://%';
