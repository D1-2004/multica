DROP INDEX IF EXISTS agent_source_managed_rollout_idx;

ALTER TABLE agent_source
    DROP CONSTRAINT IF EXISTS agent_source_source_mode_check,
    DROP CONSTRAINT IF EXISTS agent_source_source_type_check;

UPDATE agent_source
SET source_type = 'managed_git',
    updated_at = now()
WHERE source_type = 'github'
  AND managed_source_key IS NOT NULL;

ALTER TABLE agent_source
    ADD CONSTRAINT agent_source_source_type_check
        CHECK (source_type IN ('github', 'managed_git')),
    ADD CONSTRAINT agent_source_source_mode_check CHECK (
        (source_type = 'github' AND managed_source_key IS NULL)
        OR (source_type = 'managed_git' AND github_installation_id IS NULL AND managed_source_key IS NOT NULL)
    );

CREATE INDEX agent_source_managed_rollout_idx
    ON agent_source(managed_source_key, synced_commit_sha)
    WHERE source_type = 'managed_git';
