CREATE TABLE platform_template_seed (
    system_key TEXT PRIMARY KEY,
    release_version BIGINT NOT NULL CHECK (release_version > 0),
    display_name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    content_hash TEXT NOT NULL,
    bundle_schema_version INTEGER NOT NULL DEFAULT 1 CHECK (bundle_schema_version = 1),
    bundle_size_bytes INTEGER NOT NULL CHECK (bundle_size_bytes > 0 AND bundle_size_bytes <= 33554432),
    bundle JSONB NOT NULL CHECK (jsonb_typeof(bundle) = 'object'),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE agent_template (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    slug TEXT NOT NULL CHECK (slug ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'),
    display_name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    source_type TEXT NOT NULL CHECK (source_type IN ('platform', 'github')),
    management_mode TEXT NOT NULL CHECK (management_mode IN ('system_managed', 'workspace_managed')),
    system_key TEXT,
    bundle_schema_version INTEGER NOT NULL DEFAULT 1 CHECK (bundle_schema_version = 1),
    bundle_size_bytes INTEGER NOT NULL CHECK (bundle_size_bytes > 0 AND bundle_size_bytes <= 33554432),
    bundle JSONB NOT NULL CHECK (jsonb_typeof(bundle) = 'object'),
    content_hash TEXT NOT NULL,
    created_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, slug),
    UNIQUE (workspace_id, id),
    CHECK ((source_type = 'platform' AND management_mode = 'system_managed' AND system_key IS NOT NULL)
        OR (source_type = 'github' AND management_mode = 'workspace_managed' AND system_key IS NULL))
);

CREATE UNIQUE INDEX agent_template_workspace_system_key_unique
    ON agent_template(workspace_id, system_key)
    WHERE system_key IS NOT NULL;

ALTER TABLE github_installation
    ADD CONSTRAINT github_installation_workspace_id_id_unique UNIQUE (workspace_id, id);

CREATE TABLE agent_template_github_source (
    template_id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL,
    github_installation_id UUID,
    repo_owner TEXT NOT NULL,
    repo_name TEXT NOT NULL,
    ref TEXT NOT NULL,
    synced_commit_sha TEXT NOT NULL,
    sync_status TEXT NOT NULL DEFAULT 'ready' CHECK (sync_status IN ('ready', 'failed', 'disconnected')),
    last_sync_error TEXT,
    last_sync_attempt_at TIMESTAMPTZ,
    last_synced_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (workspace_id, template_id)
        REFERENCES agent_template(workspace_id, id) ON DELETE CASCADE,
    FOREIGN KEY (workspace_id, github_installation_id)
        REFERENCES github_installation(workspace_id, id) ON DELETE SET NULL (github_installation_id)
);

CREATE INDEX agent_template_workspace_idx ON agent_template(workspace_id);
CREATE INDEX agent_template_github_installation_idx
    ON agent_template_github_source(github_installation_id);
