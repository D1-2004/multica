-- Failed or held turns of a scene, newest first, for the employee scene status
-- block (Host facts frozen into new foreground snapshots). Failures are rare,
-- so the partial index stays small and adds almost no write cost.
CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_scene_job_failure_idx ON employee_scene_job (workspace_id, agent_id, tenant_org_id, scene_id, created_at DESC) WHERE state = 'completed' AND (outcome ? 'held_reason' OR outcome ? 'failure' OR outcome ? 'rescue');
