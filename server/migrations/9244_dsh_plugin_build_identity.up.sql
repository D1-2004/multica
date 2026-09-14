CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS dsh_plugin_build_identity ON dsh_plugin_build (workspace_id, build_key);
