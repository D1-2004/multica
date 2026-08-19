ALTER TABLE a2a_task_turn
    ADD COLUMN IF NOT EXISTS request_bound_lease_expires_at TIMESTAMPTZ;

ALTER TABLE a2a_task_turn
    DROP CONSTRAINT IF EXISTS a2a_task_turn_control_signal_check;

ALTER TABLE a2a_task_turn
    ADD CONSTRAINT a2a_task_turn_control_signal_check
    CHECK (control_signal IS NULL OR control_signal IN ('input_required', 'auth_required', 'request_bound'));

CREATE INDEX IF NOT EXISTS idx_a2a_task_turn_request_bound_lease
    ON a2a_task_turn (request_bound_lease_expires_at, local_task_id)
    WHERE request_bound_lease_expires_at IS NOT NULL
      AND completed_at IS NULL;
