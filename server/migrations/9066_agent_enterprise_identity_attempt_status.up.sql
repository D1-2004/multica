ALTER TABLE agent_enterprise_identity_attempt
    ADD COLUMN IF NOT EXISTS completion_status TEXT,
    ADD COLUMN IF NOT EXISTS completion_error_code TEXT,
    ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ;

UPDATE agent_enterprise_identity_attempt
SET completion_status = CASE
        WHEN consumed_at IS NULL THEN 'created'
        ELSE 'failed'
    END,
    completion_error_code = CASE
        WHEN consumed_at IS NULL THEN NULL
        ELSE 'interrupted'
    END,
    completed_at = CASE
        WHEN consumed_at IS NULL THEN NULL
        ELSE consumed_at
    END
WHERE completion_status IS NULL;

ALTER TABLE agent_enterprise_identity_attempt
    ALTER COLUMN completion_status SET DEFAULT 'created',
    ALTER COLUMN completion_status SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'agent_enterprise_identity_attempt_completion_status_check'
          AND conrelid = 'agent_enterprise_identity_attempt'::regclass
    ) THEN
        ALTER TABLE agent_enterprise_identity_attempt
            ADD CONSTRAINT agent_enterprise_identity_attempt_completion_status_check
            CHECK (completion_status IN ('created', 'pending', 'succeeded', 'failed'));
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'agent_enterprise_identity_attempt_completion_result_check'
          AND conrelid = 'agent_enterprise_identity_attempt'::regclass
    ) THEN
        ALTER TABLE agent_enterprise_identity_attempt
            ADD CONSTRAINT agent_enterprise_identity_attempt_completion_result_check
            CHECK (
                (
                    completion_status = 'created'
                    AND completion_error_code IS NULL
                    AND completed_at IS NULL
                )
                OR (
                    completion_status = 'pending'
                    AND consumed_at IS NOT NULL
                    AND completion_error_code IS NULL
                    AND completed_at IS NULL
                )
                OR (
                    completion_status = 'succeeded'
                    AND completion_error_code IS NULL
                    AND completed_at IS NOT NULL
                )
                OR (
                    completion_status = 'failed'
                    AND completion_error_code IS NOT NULL
                    AND completion_error_code IN (
                        'needs_reauthorization',
                        'employee_conflict',
                        'timed_out',
                        'binding_in_progress',
                        'internal_error',
                        'interrupted'
                    )
                    AND completed_at IS NOT NULL
                )
            );
    END IF;
END
$$;

CREATE UNIQUE INDEX IF NOT EXISTS agent_enterprise_identity_attempt_one_pending_idx
    ON agent_enterprise_identity_attempt (workspace_id, agent_id)
    WHERE consumed_at IS NOT NULL
      AND completion_status = 'pending';
