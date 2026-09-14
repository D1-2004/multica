CREATE SEQUENCE IF NOT EXISTS dsh_profile_revision_seq;
CREATE TABLE IF NOT EXISTS dsh_employee_profile (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    desired_revision bigint NOT NULL DEFAULT 0,
    applied_revision bigint NOT NULL DEFAULT 0,
    applied_generation bigint NOT NULL DEFAULT 0,
    applied_sandbox_id text NOT NULL DEFAULT '',
    applied_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS dsh_profile_revision (
    workspace_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    revision bigint NOT NULL DEFAULT nextval('dsh_profile_revision_seq'),
    template_id text NOT NULL,
    source_json text NOT NULL,
    source_digest text NOT NULL,
    descriptor_json text NOT NULL DEFAULT '',
    descriptor_digest text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS dsh_plugin_build (
    workspace_id uuid NOT NULL,
    build_key text NOT NULL,
    id uuid NOT NULL,
    plugin_id uuid NOT NULL,
    template_id text NOT NULL,
    package_name text NOT NULL,
    package_version text NOT NULL,
    package_integrity text NOT NULL,
    source_kind text NOT NULL,
    source_spec text NOT NULL,
    source_artifact_key text NOT NULL,
    state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'ready', 'failed')),
    build_digest text NOT NULL DEFAULT '',
    artifact_key text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (state <> 'ready' OR (build_digest ~ '^[a-f0-9]{64}$' AND artifact_key <> ''))
);
