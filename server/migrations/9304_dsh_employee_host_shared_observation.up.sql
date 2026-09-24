ALTER TABLE dsh_employee_host
    ADD COLUMN IF NOT EXISTS observed_shared_volume text,
    ADD COLUMN IF NOT EXISTS observed_shared_access text,
    ADD COLUMN IF NOT EXISTS observed_grant_generation bigint;
