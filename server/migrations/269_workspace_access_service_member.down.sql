-- Membership is deliberately retained on rollback. Deleting it could orphan
-- resources created by the service user after this migration was applied.
ALTER TABLE workspace_access_token
    ADD COLUMN IF NOT EXISTS capabilities TEXT[];

ALTER TABLE workspace_access_token
    ADD COLUMN IF NOT EXISTS resource_scope TEXT;

UPDATE workspace_access_token
SET capabilities = ARRAY['deployment.manage', 'deployment.retire', 'trace.read']::TEXT[]
WHERE capabilities IS NULL;

ALTER TABLE workspace_access_token
    ALTER COLUMN capabilities SET NOT NULL;

UPDATE workspace_access_token
SET resource_scope = 'own_agents'
WHERE resource_scope IS NULL;

ALTER TABLE workspace_access_token
    ALTER COLUMN resource_scope SET DEFAULT 'own_agents',
    ALTER COLUMN resource_scope SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'workspace_access_token_capabilities_nonempty'
          AND conrelid = 'workspace_access_token'::regclass
    ) THEN
        ALTER TABLE workspace_access_token
            ADD CONSTRAINT workspace_access_token_capabilities_nonempty
            CHECK (cardinality(capabilities) > 0);
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'workspace_access_token_capabilities_allowed'
          AND conrelid = 'workspace_access_token'::regclass
    ) THEN
        ALTER TABLE workspace_access_token
            ADD CONSTRAINT workspace_access_token_capabilities_allowed
            CHECK (capabilities <@ ARRAY['deployment.manage', 'deployment.retire', 'trace.read']::TEXT[]);
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'workspace_access_token_resource_scope_check'
          AND conrelid = 'workspace_access_token'::regclass
    ) THEN
        ALTER TABLE workspace_access_token
            ADD CONSTRAINT workspace_access_token_resource_scope_check
            CHECK (resource_scope IN ('own_agents', 'workspace'));
    END IF;
END $$;
