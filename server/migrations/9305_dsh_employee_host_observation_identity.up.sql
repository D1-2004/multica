ALTER TABLE dsh_employee_host
    ADD COLUMN IF NOT EXISTS observed_sandbox_id text,
    ADD COLUMN IF NOT EXISTS observed_host_generation bigint;
