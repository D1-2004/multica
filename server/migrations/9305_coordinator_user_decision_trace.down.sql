ALTER TABLE coordinator_user_decision
    DROP COLUMN IF EXISTS trace_exported_at,
    DROP COLUMN IF EXISTS trace_next_attempt_at;
