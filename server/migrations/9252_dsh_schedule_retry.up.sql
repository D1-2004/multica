ALTER TABLE dsh_schedule
    ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz,
    ADD COLUMN IF NOT EXISTS failure_count integer NOT NULL DEFAULT 0 CHECK (failure_count >= 0);
