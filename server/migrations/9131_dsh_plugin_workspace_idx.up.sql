CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS dsh_plugin_workspace_idx
    ON dsh_plugin (workspace_id, package_name);
