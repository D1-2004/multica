CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS inbound_coordinator_job_id_idx
    ON inbound_coordinator_job (id);
