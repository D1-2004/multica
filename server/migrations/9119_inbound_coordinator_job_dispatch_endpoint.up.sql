ALTER TABLE inbound_coordinator_job
    ADD COLUMN IF NOT EXISTS dispatch_endpoint_id TEXT NOT NULL DEFAULT '';
