DROP INDEX IF EXISTS idx_a2a_task_turn_request_bound_lease;

UPDATE a2a_task_turn
SET control_signal = 'auth_required',
    request_bound_lease_expires_at = NULL
WHERE control_signal = 'request_bound';

ALTER TABLE a2a_task_turn
    DROP CONSTRAINT IF EXISTS a2a_task_turn_control_signal_check;

ALTER TABLE a2a_task_turn
    ADD CONSTRAINT a2a_task_turn_control_signal_check
    CHECK (control_signal IS NULL OR control_signal IN ('input_required', 'auth_required'));

ALTER TABLE a2a_task_turn
    DROP COLUMN IF EXISTS request_bound_lease_expires_at;
