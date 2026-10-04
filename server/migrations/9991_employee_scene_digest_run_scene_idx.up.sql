CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_scene_digest_run_scene_idx ON employee_scene_digest_run (workspace_id, agent_id, scene_id, started_at DESC);
