-- GitHub-backed agent definitions are materialized into the existing agent and
-- skill tables. These additive tables only track the external source and which
-- skills are managed by it; task dispatch continues to read the active tables.

CREATE TABLE IF NOT EXISTS agent_source (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id UUID NOT NULL UNIQUE REFERENCES agent(id) ON DELETE CASCADE,
    source_type TEXT NOT NULL DEFAULT 'github'
        CHECK (source_type IN ('github')),
    github_installation_id UUID REFERENCES github_installation(id) ON DELETE SET NULL,
    repo_owner TEXT NOT NULL,
    repo_name TEXT NOT NULL,
    ref TEXT NOT NULL,
    manifest_path TEXT NOT NULL DEFAULT 'multica-agent.yaml',
    synced_commit_sha TEXT NOT NULL,
    sync_status TEXT NOT NULL DEFAULT 'ready'
        CHECK (sync_status IN ('ready', 'failed', 'disconnected')),
    last_sync_error TEXT,
    last_sync_attempt_at TIMESTAMPTZ,
    last_synced_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by UUID REFERENCES "user"(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_agent_source_github_installation
    ON agent_source(github_installation_id);

CREATE INDEX IF NOT EXISTS idx_agent_source_repository
    ON agent_source(repo_owner, repo_name);

CREATE TABLE IF NOT EXISTS agent_source_skill (
    agent_source_id UUID NOT NULL REFERENCES agent_source(id) ON DELETE CASCADE,
    skill_id UUID NOT NULL UNIQUE REFERENCES skill(id) ON DELETE CASCADE,
    source_path TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_source_id, source_path)
);

CREATE INDEX IF NOT EXISTS idx_agent_source_skill_source
    ON agent_source_skill(agent_source_id);
