-- A preview is a server-owned, immutable candidate read from one Git commit.
-- Applied previews also serve as release receipts; no archive upload is used.
CREATE TABLE IF NOT EXISTS agent_source_preview (
    id UUID NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    created_by UUID NOT NULL,
    agent_id UUID,
    agent_source_id UUID,
    github_installation_id UUID NOT NULL,
    repository TEXT NOT NULL,
    ref TEXT NOT NULL,
    resolved_sha TEXT NOT NULL,
    expected_source_sha TEXT NOT NULL DEFAULT '',
    expected_state_hash TEXT NOT NULL DEFAULT '',
    snapshot JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT now() + interval '30 minutes',
    applied_at TIMESTAMPTZ,
    applied_source JSONB,
    applied_changed BOOLEAN NOT NULL DEFAULT FALSE,
    CONSTRAINT agent_source_preview_snapshot_size CHECK (octet_length(snapshot::text) <= 67108864)
);
