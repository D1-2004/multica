DROP INDEX IF EXISTS agent_enterprise_identity_attempt_one_pending_idx;

ALTER TABLE agent_enterprise_identity_attempt
    DROP CONSTRAINT IF EXISTS agent_enterprise_identity_attempt_completion_result_check,
    DROP CONSTRAINT IF EXISTS agent_enterprise_identity_attempt_completion_status_check,
    DROP COLUMN IF EXISTS completed_at,
    DROP COLUMN IF EXISTS completion_error_code,
    DROP COLUMN IF EXISTS completion_status;
