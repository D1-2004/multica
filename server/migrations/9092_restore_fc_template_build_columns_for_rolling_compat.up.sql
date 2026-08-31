ALTER TABLE fc_e2b_stable_release
    ADD COLUMN IF NOT EXISTS template_build_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS previous_template_build_id TEXT NOT NULL DEFAULT '';

ALTER TABLE fc_e2b_stable_release
    ALTER COLUMN template_build_id SET DEFAULT '',
    ALTER COLUMN previous_template_build_id SET DEFAULT '';

ALTER TABLE fc_e2b_stable_channel
    ADD COLUMN IF NOT EXISTS current_template_build_id TEXT NOT NULL DEFAULT '';

ALTER TABLE fc_e2b_stable_channel
    ALTER COLUMN current_template_build_id SET DEFAULT '';

ALTER TABLE fc_e2b_stable_release_target
    ADD COLUMN IF NOT EXISTS previous_template_build_id TEXT NOT NULL DEFAULT '';

ALTER TABLE fc_e2b_stable_release_target
    ALTER COLUMN previous_template_build_id SET DEFAULT '';
