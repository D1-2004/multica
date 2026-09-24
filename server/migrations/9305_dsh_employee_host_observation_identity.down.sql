ALTER TABLE dsh_employee_host
    DROP COLUMN IF EXISTS observed_host_generation,
    DROP COLUMN IF EXISTS observed_sandbox_id;
