-- DSH plugins as a workspace asset, mirroring how DeepSeek Harness itself
-- models them: a plugin is an npm package a profile lists in
-- `dsh.profile.bundles`, and its settings live on the loader row its own
-- cordis.patch.yml declares. `dsh plugin add` is a pnpm passthrough, so a
-- pinned package reference is the whole of the install contract.
--
-- No PRIMARY KEY / FOREIGN KEY: indexes are created CONCURRENTLY in follow-up
-- files, and dependent cleanup is explicit in application code.

CREATE TABLE IF NOT EXISTS dsh_plugin (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,

    -- The npm package name. This is the identity DSH itself uses.
    package_name TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    homepage TEXT NOT NULL DEFAULT '',

    -- How to fetch it, in the vocabulary the runtime adapter already accepts.
    source_kind TEXT NOT NULL DEFAULT 'npm'
        CHECK (source_kind IN ('npm', 'github', 'url', 'file')),
    source_spec TEXT NOT NULL,
    resolved_version TEXT NOT NULL DEFAULT '',
    -- sha256-<64 hex>, or '' when the source is not pinned.
    integrity TEXT NOT NULL DEFAULT ''
        CHECK (integrity = '' OR integrity ~ '^sha256-[0-9a-f]{64}$'),

    -- Loader rows the package's own bundle patch declares, and which of them
    -- `config` overrides. A row id need not equal the package name.
    bundle_rows JSONB NOT NULL DEFAULT '[]'::jsonb
        CHECK (jsonb_typeof(bundle_rows) = 'array' AND pg_column_size(bundle_rows) <= 8192),
    config_row TEXT NOT NULL DEFAULT '',
    config JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(config) = 'object' AND pg_column_size(config) <= 65536),

    -- Provenance: which community catalog this came from, if any. DeepSeek
    -- publishes no catalog of its own, so this is always a community source
    -- and must never be presented as official.
    catalog TEXT NOT NULL DEFAULT '',
    -- DSH declares no compatibility field and enforces no version check, so a
    -- plugin built against an older harness installs cleanly and fails at
    -- boot. Recording what it was last validated against is the only warning
    -- we can give before that happens.
    validated_dsh_version TEXT NOT NULL DEFAULT '',
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS agent_dsh_plugin (
    agent_id UUID NOT NULL,
    dsh_plugin_id UUID NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A cache of a public, CC0-licensed community catalog. Public data, so it is
-- not workspace-scoped. A refresh replaces it wholesale inside one
-- transaction under an advisory lock, so catalog_version always describes a
-- complete snapshot rather than however far a failed run got.
CREATE TABLE IF NOT EXISTS dsh_plugin_catalog_entry (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    catalog TEXT NOT NULL DEFAULT 'awesome-dsh-plugin',
    name TEXT NOT NULL,
    owner TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    page TEXT NOT NULL DEFAULT '',
    category TEXT NOT NULL DEFAULT '',
    description_en TEXT NOT NULL DEFAULT '',
    description_zh TEXT NOT NULL DEFAULT '',
    npm_package TEXT NOT NULL DEFAULT '',
    npm_version TEXT NOT NULL DEFAULT '',
    tarball_url TEXT NOT NULL DEFAULT '',
    stars INTEGER NOT NULL DEFAULT 0,
    downloads BIGINT NOT NULL DEFAULT 0,
    install_hint TEXT NOT NULL DEFAULT '',
    added_on DATE,
    catalog_version TEXT NOT NULL DEFAULT '',
    refreshed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
