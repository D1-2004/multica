CREATE INDEX CONCURRENTLY IF NOT EXISTS inbound_coordinator_job_claim_idx
    ON inbound_coordinator_job (available_at, created_at)
    WHERE status IN ('pending', 'running');
