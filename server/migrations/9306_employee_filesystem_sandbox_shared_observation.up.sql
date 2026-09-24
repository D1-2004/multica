ALTER TABLE employee_filesystem_sandbox
    ADD COLUMN IF NOT EXISTS observed_shared_volume text,
    ADD COLUMN IF NOT EXISTS observed_shared_access text,
    ADD COLUMN IF NOT EXISTS observed_grant_generation bigint,
    ADD COLUMN IF NOT EXISTS observed_sandbox_id text,
    ADD COLUMN IF NOT EXISTS observed_host_generation bigint;
