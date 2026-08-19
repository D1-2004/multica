ALTER TABLE fc_e2b_stable_release_target
    DROP CONSTRAINT IF EXISTS fc_e2b_stable_release_target_provider_check;

-- Keep rows created by the five-provider release readable during rollback,
-- while rejecting new DSH/OpenCode v2 targets from the rolled-back service.
ALTER TABLE fc_e2b_stable_release_target
    ADD CONSTRAINT fc_e2b_stable_release_target_provider_check
    CHECK (provider IN ('hermes', 'opencode', 'pi'))
    NOT VALID;
