ALTER TABLE dsh_employee_profile
    DROP COLUMN IF EXISTS next_apply_at,
    DROP COLUMN IF EXISTS apply_attempts,
    DROP COLUMN IF EXISTS apply_error;
