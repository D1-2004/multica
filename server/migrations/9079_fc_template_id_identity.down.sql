ALTER TABLE fc_e2b_stable_release
    ADD COLUMN template_build_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN previous_template_build_id TEXT NOT NULL DEFAULT '';

ALTER TABLE fc_e2b_stable_channel
    ADD COLUMN current_template_build_id TEXT NOT NULL DEFAULT '';

ALTER TABLE fc_e2b_stable_release_target
    ADD COLUMN previous_template_build_id TEXT NOT NULL DEFAULT '';

ALTER TABLE fc_e2b_stable_release_target
    ADD CONSTRAINT fc_e2b_stable_release_target_provider_check CHECK (
        provider IN ('hermes', 'opencode', 'pi', 'dsh', 'opencode-v2')
    );
