CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS employee_scene_ledger_identity_idx ON employee_scene_ledger (workspace_id, agent_id, scene_id, entry_kind, source_id);
