DROP INDEX IF EXISTS agent_source_managed_rollout_idx;
DROP INDEX IF EXISTS agent_source_workspace_managed_key_idx;

ALTER TABLE agent_source
    DROP CONSTRAINT IF EXISTS agent_source_source_mode_check,
    DROP CONSTRAINT IF EXISTS agent_source_source_type_check,
    ADD CONSTRAINT agent_source_source_type_check CHECK (source_type IN ('github')),
    DROP COLUMN IF EXISTS managed_source_key,
    DROP COLUMN IF EXISTS workspace_id;

DROP TABLE IF EXISTS managed_agent_source_snapshot;
