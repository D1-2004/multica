-- Resource access now follows the token subject's native ownership. Keep the
-- legacy column during rolling deployments, but normalize previously created
-- workspace-wide tokens so old readers cannot retain widened access.
UPDATE workspace_access_token
SET resource_scope = 'own_agents',
    updated_at = now()
WHERE resource_scope <> 'own_agents';
