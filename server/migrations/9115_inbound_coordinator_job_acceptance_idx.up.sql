CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS inbound_coordinator_job_acceptance_idx
    ON inbound_coordinator_job (acceptance_id);
