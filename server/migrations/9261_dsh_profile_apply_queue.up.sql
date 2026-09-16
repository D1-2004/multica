ALTER TABLE dsh_employee_profile
    ADD COLUMN IF NOT EXISTS next_apply_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN IF NOT EXISTS apply_attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS apply_error text NOT NULL DEFAULT '';
