CREATE TABLE IF NOT EXISTS hosted_site (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    public_id TEXT NOT NULL,
    workspace_id UUID NOT NULL,
    owner_agent_id UUID NOT NULL,
    active_revision_id UUID,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'deleted')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS hosted_site_revision (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    site_id UUID NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'uploading', 'active', 'failed')),
    entrypoint TEXT NOT NULL DEFAULT 'index.html',
    spa_fallback BOOLEAN NOT NULL DEFAULT false,
    manifest JSONB NOT NULL DEFAULT '{}'::jsonb,
    archive_sha256 TEXT,
    file_count INTEGER NOT NULL DEFAULT 0,
    total_bytes BIGINT NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    activated_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS hosted_site_upload (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    site_id UUID NOT NULL,
    revision_id UUID NOT NULL,
    token_hash BYTEA NOT NULL,
    expected_sha256 TEXT NOT NULL,
    expected_length BIGINT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
