CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS agent_scene_locator_idx ON agent_scene (workspace_id, agent_id, provider, tenant_org_id, source_namespace, external_scene_id);
