ALTER TABLE employee_filesystem_sandbox
    DROP COLUMN IF EXISTS observed_host_generation,
    DROP COLUMN IF EXISTS observed_sandbox_id,
    DROP COLUMN IF EXISTS observed_grant_generation,
    DROP COLUMN IF EXISTS observed_shared_access,
    DROP COLUMN IF EXISTS observed_shared_volume;
