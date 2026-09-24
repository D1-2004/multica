ALTER TABLE coordinator_user_decision
    ADD COLUMN IF NOT EXISTS trace_exported_at timestamptz,
    ADD COLUMN IF NOT EXISTS trace_next_attempt_at timestamptz;
