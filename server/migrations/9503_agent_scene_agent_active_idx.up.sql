CREATE INDEX CONCURRENTLY IF NOT EXISTS agent_scene_agent_active_idx ON agent_scene (workspace_id, agent_id, tenant_org_id, last_active_at DESC, id);
