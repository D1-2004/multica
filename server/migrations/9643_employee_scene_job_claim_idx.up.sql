CREATE INDEX CONCURRENTLY IF NOT EXISTS employee_scene_job_claim_idx ON employee_scene_job (available_at, created_at, id) WHERE state <> 'completed';
