-- DTA and the public Agent Source API expose one Git-backed source contract.
-- Platform-synchronized sources remain distinguishable by managed_source_key,
-- without a private managed_git source type leaking into the shared catalog.

DROP INDEX IF EXISTS agent_source_managed_rollout_idx;

ALTER TABLE agent_source
    DROP CONSTRAINT IF EXISTS agent_source_source_mode_check,
    DROP CONSTRAINT IF EXISTS agent_source_source_type_check;

UPDATE agent_source
SET source_type = 'github',
    updated_at = now()
WHERE source_type = 'managed_git'
  AND managed_source_key IS NOT NULL;

ALTER TABLE agent_source
    ADD CONSTRAINT agent_source_source_type_check
        CHECK (source_type IN ('github')),
    ADD CONSTRAINT agent_source_source_mode_check CHECK (
        source_type = 'github'
        AND (
            managed_source_key IS NULL
            OR (managed_source_key IS NOT NULL AND github_installation_id IS NULL)
        )
    );

CREATE INDEX agent_source_managed_rollout_idx
    ON agent_source(managed_source_key, synced_commit_sha)
    WHERE managed_source_key IS NOT NULL;
