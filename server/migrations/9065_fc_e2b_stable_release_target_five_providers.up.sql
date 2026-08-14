ALTER TABLE fc_e2b_stable_release_target
    DROP CONSTRAINT IF EXISTS fc_e2b_stable_release_target_provider_check;

ALTER TABLE fc_e2b_stable_release_target
    ADD CONSTRAINT fc_e2b_stable_release_target_provider_check
    CHECK (provider IN ('hermes', 'opencode', 'pi', 'dsh', 'opencode-v2'))
    NOT VALID;

ALTER TABLE fc_e2b_stable_release_target
    VALIDATE CONSTRAINT fc_e2b_stable_release_target_provider_check;
