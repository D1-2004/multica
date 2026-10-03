CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_scene_ledger_time_idx ON employee_scene_ledger (workspace_id, agent_id, scene_id, occurred_at DESC);
