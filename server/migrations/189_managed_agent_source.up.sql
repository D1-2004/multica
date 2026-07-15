-- Platform-managed Agents use the same materialized Agent/Skill tables as
-- Git-backed Agents, but their source is one deployment-owned public Git repo.
-- The compiled bundle is a PostgreSQL last-known-good snapshot so request
-- handling never depends on a node-local checkout or live Git availability.

CREATE TABLE IF NOT EXISTS managed_agent_source_snapshot (
    source_key TEXT PRIMARY KEY,
    repository_url TEXT NOT NULL,
    ref TEXT NOT NULL,
    resolved_commit_sha TEXT,
    bundle_hash TEXT,
    bundle JSONB,
    last_check_at TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT managed_agent_source_snapshot_complete_bundle_check CHECK (
        (bundle IS NULL AND resolved_commit_sha IS NULL AND bundle_hash IS NULL)
        OR
        (bundle IS NOT NULL AND resolved_commit_sha IS NOT NULL AND bundle_hash IS NOT NULL)
    ),
    CONSTRAINT managed_agent_source_snapshot_bundle_size_check CHECK (
        bundle IS NULL OR pg_column_size(bundle) <= 33554432
    )
);

ALTER TABLE agent_source
    ADD COLUMN IF NOT EXISTS workspace_id UUID REFERENCES workspace(id) ON DELETE CASCADE,
    ADD COLUMN IF NOT EXISTS managed_source_key TEXT REFERENCES managed_agent_source_snapshot(source_key) ON DELETE RESTRICT;

UPDATE agent_source AS source
SET workspace_id = agent.workspace_id
FROM agent
WHERE source.agent_id = agent.id
  AND source.workspace_id IS NULL;

ALTER TABLE agent_source
    ALTER COLUMN workspace_id SET NOT NULL,
    DROP CONSTRAINT IF EXISTS agent_source_source_mode_check,
    DROP CONSTRAINT IF EXISTS agent_source_source_type_check,
    ADD CONSTRAINT agent_source_source_type_check
        CHECK (source_type IN ('github', 'managed_git')),
    ADD CONSTRAINT agent_source_source_mode_check CHECK (
        (source_type = 'github' AND managed_source_key IS NULL)
        OR (source_type = 'managed_git' AND github_installation_id IS NULL AND managed_source_key IS NOT NULL)
    );

CREATE UNIQUE INDEX IF NOT EXISTS agent_source_workspace_managed_key_idx
    ON agent_source(workspace_id, managed_source_key)
    WHERE managed_source_key IS NOT NULL;

CREATE INDEX IF NOT EXISTS agent_source_managed_rollout_idx
    ON agent_source(managed_source_key, synced_commit_sha)
    WHERE source_type = 'managed_git';
