ALTER TABLE dsh_employee_host
    DROP COLUMN IF EXISTS observed_grant_generation,
    DROP COLUMN IF EXISTS observed_shared_access,
    DROP COLUMN IF EXISTS observed_shared_volume;
