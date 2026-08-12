-- Resource access now follows the token subject's native ownership. Keep the
-- legacy column during rolling deployments, but normalize previously created
-- workspace-wide tokens so old readers cannot retain widened access.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'workspace_access_token'
          AND column_name = 'resource_scope'
    ) THEN
        UPDATE workspace_access_token
        SET resource_scope = 'own_agents',
            updated_at = now()
        WHERE resource_scope <> 'own_agents';
    END IF;
END $$;
